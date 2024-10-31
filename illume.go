package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	fp "path/filepath"
	"strings"
)

const (
	DefaultProfile = "llama.cpp"
)

var Profiles = map[string][]string{
	"llama.cpp": []string{
		"!api http://localhost:8080/v1",
		"!:cache_prompt true",
	},
	"huggingface.co": []string{
		"!api https://api-inference.huggingface.co/models/{model}/v1",
		"!:model Qwen/Qwen2.5-72B-Instruct",
		"!>x-use-cache false",
	},
}

func addfile(w *bytes.Buffer, path string, name string) error {
	w.WriteString("**`")
	w.WriteString(name)
	w.WriteString("`**\n```\n")

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(w, f) // FIXME: filter nested code fences

	w.WriteString("```\n\n")
	return err
}

func addcontext(prompt *bytes.Buffer, line string) error {
	fields := strings.Fields(line)
	switch len(fields) {
	case 0, 1:
		return fmt.Errorf("!context: wrong number of fields")
	case 2:
		return addfile(prompt, fields[1], fields[1])
	}

	dir := fields[1]
	cut := len(dir)
	for cut > 0 && dir[cut-1] != '/' && dir[cut-1] != '\\' {
		cut--
	}

	return fp.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		for _, suffix := range fields[2:] {
			if strings.HasSuffix(path, suffix) {
				name := path[cut:]
				addfile(prompt, path, name)
				break
			}
		}

		return nil
	})
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Choice struct {
	Message Message `json:"message"`
}

type Response struct {
	Content string
	Choices []struct {
		Text  string
		Delta struct {
			Content string
		}
	}
}

type Builder struct {
	Messages []Message
	Role     string
	Content  bytes.Buffer
}

func (b *Builder) Append(line string) {
	b.Content.WriteString(line)
	b.Content.WriteString("\n")
}

func (b *Builder) New(role string) []Message {
	content := strings.TrimSpace(b.Content.String())
	if content != "" {
		if b.Role == "" {
			b.Role = "system"
		}
		b.Messages = append(b.Messages, Message{b.Role, content})
	}
	b.Role = role
	b.Content = bytes.Buffer{}
	if len(b.Messages) == 0 {
		return []Message{}
	}
	return b.Messages
}

func cut(s string, b byte) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return s[:i], s[i+1:], true
		}
	}
	return s, s[len(s):], false
}

func interpolate(s string, vars map[string]interface{}) (string, error) {
	var b bytes.Buffer

	for {
		pre, key, ok := cut(s, '{')
		b.WriteString(pre)
		if !ok {
			return b.String(), nil
		}

		key, s, ok = cut(key, '}')
		if !ok {
			return "", fmt.Errorf("unmatched '}'")
		}

		value, ok := vars[key]
		if !ok {
			return "", fmt.Errorf("missing key: %s", key)
		}

		svalue, ok := value.(string)
		if ok {
			b.WriteString(svalue)
		} else {
			r, _ := json.Marshal(value)
			b.Write(r)
		}
	}
}

type ChatState struct {
	Profile string
	Api     string
	Builder Builder
	Data    map[string]interface{}
	UserSet map[string]bool
	Headers map[string]string
	Debug   bool
	Chat    bool
}

func NewChatState(token string) *ChatState {
	s := &ChatState{
		Api: "http://invalid./",
		Data: map[string]interface{}{
			"max_tokens": 1000,
		},
		UserSet: map[string]bool{},
		Chat:    true,
		Headers: map[string]string{
			"content-type": "application/json",
		},
	}

	if token != "" {
		s.Headers["authorization"] = "Bearer " + token
	}

	return s
}

func (s *ChatState) LoadProfile(profile string, depth int) error {
	var body string
	if lines, ok := Profiles[profile]; ok {
		var buf bytes.Buffer
		for _, line := range lines {
			buf.WriteString(line)
			buf.WriteString("\n")
		}
		body = buf.String()
	} else {

		buf, err := ioutil.ReadFile(profile)
		if err != nil {
			return err
		}
		body = string(buf)
	}
	s.Profile = profile
	return s.Load(profile, body, depth+1) // may recurse
}

func (s *ChatState) Load(name, txt string, depth int) error {
	lineno := 1
	for line, lines := txt, txt; len(lines) > 0; lineno++ {
		line, lines, _ = cut(lines, '\n')
		command, args, _ := cut(line, ' ')

		if strings.HasPrefix(line, "!!") {
			line = line[1:] // escape "!!" as "!"

		} else if command == "!profile" {
			profile := strings.TrimSpace(args)
			if err := s.LoadProfile(profile, depth); err != nil {
				return fmt.Errorf("%s:%d: %w", name, lineno, err)
			}
			continue

		} else if command == "!token" {
			token := strings.TrimSpace(args)
			if token == "" {
				delete(s.Headers, "authorization")
			} else {
				s.Headers["authorization"] = "Bearer " + token
			}
			continue

		} else if command == "!debug" {
			s.Debug = true
			continue

		} else if command == "!completion" {
			s.Chat = false
			continue

		} else if command == "!context" {
			if err := addcontext(&s.Builder.Content, line); err != nil {
				return fmt.Errorf("%s:%d: %w", name, lineno, err)
			}
			continue

		} else if command == "!note" {
			// used for comments
			continue

		} else if command == "!begin" {
			s.Builder = Builder{}
			continue

		} else if command == "!end" {
			break

		} else if command == "!api" {
			s.Api = strings.TrimSpace(args)
			continue

		} else if command == "!assistant" || command == "!user" {
			s.Builder.New(command[1:])
			continue

		} else if len(command) > 2 && command[:2] == "!>" {
			key := command[2:]
			args = strings.TrimSpace(args)
			if args == "" {
				delete(s.Headers, key)
			} else {
				s.Headers[key] = args
			}
			continue

		} else if len(command) > 2 && command[:2] == "!:" {
			key := command[2:]
			if depth > 0 {
				if hard, ok := s.UserSet[key]; ok && hard {
					continue // do not override
				}
			}

			args = strings.TrimSpace(args)
			if args == "" {
				delete(s.Data, key)
			} else {
				var value interface{}
				err := json.Unmarshal(([]byte)(args), &value)
				if err != nil {
					s.Data[key] = args
				} else {
					s.Data[key] = value
				}
			}
			s.UserSet[key] = depth == 0
			continue
		}

		s.Builder.Append(line)
	}

	return nil
}

func query(profile, txt, token string) error {
	var (
		client http.Client
		state  = NewChatState(token)
	)

	if err := state.Load("<stdin>", txt, 0); err != nil {
		return err
	}

	if state.Profile == "" {
		// No profile loaded yet. Load one now.
		if profile == "" {
			profile = DefaultProfile
		}
		if err := state.LoadProfile(profile, 1); err != nil {
			return err
		}
	}

	api, err := interpolate(state.Api, state.Data)
	if err != nil {
		return fmt.Errorf("interpolating URL: %w", err)
	}

	if !strings.HasSuffix(api, "/") {
		api += "/"
	}

	if state.Chat {
		api += "chat/completions"
		state.Data["messages"] = state.Builder.New("")
	} else {
		api += "completions"
		state.Data["prompt"] = state.Builder.New("")[0].Content
	}

	state.Data["stream"] = true
	body, _ := json.Marshal(state.Data)

	if state.Debug {
		w := bufio.NewWriter(os.Stdout)
		fmt.Fprintf(w, "\n\nPOST %s HTTP/1.1\n", api)
		for key, value := range state.Headers {
			fmt.Fprintf(w, "%s: %s\n", key, value)
		}
		fmt.Fprintf(w, "\n%s\n", body)
		return w.Flush()
	}

	req, err := http.NewRequest("POST", api, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	for key, value := range state.Headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		ebody, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(ebody))
	}

	w := bufio.NewWriter(os.Stdout)
	if state.Chat {
		w.WriteString("\n\n!assistant\n\n")
		w.Flush()
	}

	s := bufio.NewScanner(resp.Body)
	for s.Scan() {
		line := s.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		line = line[6:]
		if bytes.Equal(line, []byte("[DONE]")) {
			break
		}

		var r Response
		json.Unmarshal(line, &r)

		// Response schemas are all over the place. Try reading from
		// three different schemas at once. Missing fields are likely
		// empty strings, and so produce no output.
		if len(r.Choices) > 0 {
			w.WriteString(r.Choices[0].Delta.Content) // chat
			w.WriteString(r.Choices[0].Text)          // completion
		} else {
			w.WriteString(r.Content) // completion
		}

		w.Flush()
	}
	if err := s.Err(); err != nil {
		return err
	}

	return w.Flush()
}

func run() error {
	body, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		return err
	}

	profile := os.Getenv("ILLUME_PROFILE")
	token := os.Getenv("ILLUME_TOKEN")
	return query(profile, string(body), token)
}

func main() {
	if err := run(); err != nil {
		fmt.Printf("\n\n!error\n\n%s\n", err)
		os.Exit(1)
	}
}
