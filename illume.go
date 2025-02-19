package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	fp "path/filepath"
	"strings"
	"time"
)

const (
	DefaultProfile = "llama.cpp"
)

var Profiles = map[string][]string{
	"llama.cpp": []string{
		"!api http://localhost:8080/v1",
		"!:cache_prompt true",
		`!:stop ["<|im_end|>"]`,
	},
	"huggingface.co": []string{
		"!api https://api-inference.huggingface.co/models/{model}/v1",
		"!:model meta-llama/Llama-3.3-70B-Instruct",
		"!>x-use-cache false",
	},
	"together": []string{
		"!api https://huggingface.co/api/inference-proxy/together/v1",
		"!:model deepseek-ai/DeepSeek-R1",
		"!profile fim:deepseek",
		"!:temperature 0.6",
		"!:max_tokens 10000",
	},

	// Fill in the Middle (FIM), ranked from best to worst.
	"fim:deepseek": []string{ // best of class, works with /infill
		"!infill <｜begin▁of▁sentence｜>" +
			"<｜fim▁begin｜>{prefix}<｜fim▁hole｜>{suffix}<｜fim▁end｜>",
	},
	"fim:qwen": []string{ // good, also works with /infill
		"!infill <|fim_prefix|>{prefix}<|fim_suffix|>{suffix}<|fim_middle|>",
	},
	"fim:granite": []string{ // good
		"!infill <fim_prefix>{prefix}<fim_suffix>{suffix}<fim_middle>",
	},
	"fim:mistral": []string{ // specifically codestral, mediocre
		"!infill [SUFFIX]{suffix}[PREFIX]{prefix}",
	},
	"fim:codellama": []string{ // produces far too much code, poor
		"!infill  <PRE> {prefix} <SUF>{suffix} <MID>",
		`!:stop ["<EOT>"]`,
	},
	// CodeGemma is notably missing because its llama.cpp tokenizer is
	// broken, so FIM is inaccessible from the completion endpoint. It's
	// supported by the /infill endpoint, so use that instead. Results
	// are on par with CodeLlama.
}

func addfile(w *bytes.Buffer, path string, name string) error {
	body, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	txt := string(body)

	w.WriteString("**`")
	w.WriteString(name)
	w.WriteString("`**\n")

	max := 2
	for line, lines := txt, txt; len(lines) > 0; {
		line, lines, _ = cut(lines, '\n')
		n := 0
		for ; n < len(line) && line[n] == '`'; n++ {
		}
		if n > max {
			max = n
		}
	}

	for i := 0; i < max+1; i++ {
		w.WriteByte('`')
	}
	w.WriteByte('\n')
	w.WriteString(txt)
	for i := 0; i < max+1; i++ {
		w.WriteByte('`')
	}
	w.WriteString("\n\n")
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
				if err := addfile(prompt, path, name); err != nil {
					return err
				}
				break
			}
		}

		return nil
	})
}

type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []Function `json:"tool_calls,omitempty"`
}

type Function struct {
	Function ToolCall
}

type ToolCall struct {
	Name      string
	Arguments string
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
		Message      Message
		FinishReason string `json:"finish_reason"`
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
	if b.Role == "tool" {
		b.Content.WriteString("</tool_response>")
	}
	content := strings.Trim(b.Content.String(), "\r\n")
	if content != "" {
		if b.Role == "" {
			b.Role = "system"
		}
		b.Messages = append(b.Messages, Message{b.Role, content, nil})
	}
	if role == "tool" {
		b.Messages = append(b.Messages, Message{"assistant", "", nil})
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

func marshal(v interface{}) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	err := e.Encode(v)
	return b.Bytes(), err
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
			r, _ := marshal(value)
			b.Write(r)
		}
	}
}

const (
	TypeChat = iota
	TypeCompletion
	TypeInfill
	TypeFim
)

type ChatState struct {
	Profile string
	Api     string
	FimTmpl string
	Builder Builder
	Data    map[string]interface{}
	UserSet map[string]bool
	Headers map[string]string
	Tools   []map[string]interface{}
	Type    int
	Debug   bool
	Stats   bool
}

const (
	InvalidUrl = "http://invalid./"
)

func NewChatState(token string) *ChatState {
	s := &ChatState{
		Api: InvalidUrl,
		Data: map[string]interface{}{
			"max_tokens": 2000,
		},
		UserSet: map[string]bool{},
		Type:    TypeChat,
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

		} else if command == "!stats" {
			s.Stats = true
			continue

		} else if command == "!completion" {
			s.Type = TypeCompletion
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
			if s.Api == InvalidUrl || depth == 0 {
				s.Api = strings.TrimSpace(args)
			}
			continue

		} else if command == "!assistant" || command == "!user" {
			s.Builder.New(command[1:])
			continue

		} else if command == "!tool" {
			s.Builder.New("tool")
			s.Builder.Content.WriteString("<tool_response>\n")
			continue

		} else if command == "!infill" {
			if args == "" {
				if len(s.FimTmpl) > 0 {
					s.Type = TypeFim
				} else {
					s.Type = TypeInfill
				}
				s.Builder.New("infill")
			} else {
				s.FimTmpl = args
			}
			continue

		} else if strings.HasPrefix(command, "!tool:") {
			name := command[6:]
			type J map[string]interface{}
			if name == "readfile" {
				tool := J{
					"type": "function",
					"function": J{
						"name":        "readfile",
						"description": "Read the contents of a file.",
						"parameters": J{
							"type": "object",
							"properties": J{
								"path": J{
									"type":        "string",
									"description": "Path to an existing file.",
								},
							},
						},
						"required": []string{"path"},
					},
				}
				s.Tools = append(s.Tools, tool)
			}
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

func endsWithVersion(s string) bool {
	n := len(s)
	return n > 3 && s[n-3] == 'v' && s[n-2] >= '0' && s[n-2] <= '9'
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

	switch state.Type {
	case TypeChat:
		api += "chat/completions"
		state.Data["messages"] = state.Builder.New("")

	case TypeCompletion:
		api += "completions"
		state.Data["prompt"] = state.Builder.New("")[0].Content

	case TypeInfill:
		// llama.cpp only
		if !endsWithVersion(api) {
			return fmt.Errorf("cannot determine infill URL: %s", api)
		}
		api = api[:len(api)-3] + "infill"

		// TODO: Reduce the number of predicted tokens? In general, the
		// reliability of generated code follows the inverse-square law
		// by number of lines of code. Best used in short bursts. Infill
		// tends to generate extraneous, unwanted code, like "tests" and
		// examples, and maybe predicting fewer would help. Though in my
		// experiments, predicting few didn't make a difference.

		state.Data["prompt"] = "" // prompt is required

		// TODO: Consider trimming prefix/suffix? Maybe to a certain
		// number of lines to the nearest blank line. Otherwise this
		// will not work well on large source files. On the other hand
		// it might lose critical context. A smarter tool would crush
		// the context down to just declarations/prototypes.
		parts := state.Builder.New("")
		state.Data["input_prefix"] = parts[0].Content + "\n"
		if len(parts) > 1 {
			state.Data["input_suffix"] = "\n" + parts[1].Content
		} else {
			state.Data["input_suffix"] = ""
		}

	case TypeFim:
		api += "completions"

		parts := state.Builder.New("")
		vars := map[string]interface{}{
			"prefix": parts[0].Content + "\n",
			"suffix": "",
		}
		if len(parts) > 1 {
			vars["suffix"] = "\n" + parts[1].Content
		}

		state.Data["prompt"], err = interpolate(state.FimTmpl, vars)
		if err != nil {
			return fmt.Errorf("!infill: %w", err)
		}
	}

	stream := false
	if len(state.Tools) > 0 {
		state.Data["tools"] = state.Tools
	} else {
		state.Data["stream"] = true
		stream = true
	}
	body, _ := marshal(state.Data)

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

	time_start := time.Now()
	resp, err := client.Do(req)
	time_response := time.Now()
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		ebody, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(ebody))
	}

	w := bufio.NewWriter(os.Stdout)

	if !stream {
		result, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		var r Response
		json.Unmarshal(result, &r)

		if r.Choices[0].FinishReason == "tool_calls" {
			w.WriteString("\n\n!tool\n")

			for _, call := range r.Choices[0].Message.ToolCalls {
				if call.Function.Name == "readfile" {
					var args struct{ Path string }
					json.Unmarshal([]byte(call.Function.Arguments), &args)
					w.WriteString("!context ")
					w.WriteString(args.Path)
					w.WriteString("\n")
				}
			}

		} else {
			w.WriteString("\n\n!assistant\n\n")
			w.WriteString(r.Choices[0].Message.Content)
		}
		return w.Flush()
	}

	if state.Type == TypeChat {
		w.WriteString("\n\n!assistant\n\n")
		w.Flush()
	}

	nevents := 0
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
			chat := r.Choices[0].Delta.Content
			if len(chat) > 0 {
				w.WriteString(chat)
			} else {
				w.WriteString(r.Choices[0].Text)
			}
		} else {
			w.WriteString(r.Content) // completion
		}

		w.Flush()
		nevents++
	}
	if err := s.Err(); err != nil {
		return err
	}
	if err := resp.Body.Close(); err != nil {
		return err
	}
	time_done := time.Now()

	if state.Stats {
		req_time := time_response.Sub(time_start)
		stream_time := time_done.Sub(time_response)
		token_rate := float64(nevents) / stream_time.Seconds()
		fmt.Fprintf(
			w, "\n\n!note %.3g tok/s, %d toks, %v",
			token_rate, nevents, req_time,
		)
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
