package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
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
		"!api https://api-inference.huggingface.co/models/" +
			"Qwen/Qwen2.5-72B-Instruct" + "/v1",
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

	return filepath.WalkDir(dir, func(path string, info fs.DirEntry, err error) error {
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

type ChatResponse struct {
	Choices []struct {
		Delta struct {
			Content string
		}
	}
}

type CompletionResponse struct {
	Content string
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
	return b.Messages
}

func query(txt, token string) error {
	var (
		api     = "http://invalid./"
		builder Builder
		client  http.Client
		data    = map[string]any{
			"max_tokens": 1000,
		}
		debug   = false
		chat    = true
		headers = map[string]string{
			"content-type": "application/json",
		}
	)

	if token != "" {
		headers["authorization"] = "Bearer " + token
	}

	for line, lines := txt, txt; len(lines) > 0; {
		line, lines, _ = strings.Cut(lines, "\n")
		command, args, _ := strings.Cut(line, " ")

		if strings.HasPrefix(line, "!!") {
			line = line[1:] // escape "!!" as "!"

		} else if command == "!debug" {
			debug = true
			continue

		} else if command == "!completion" {
			chat = false
			continue

		} else if command == "!context" {
			if err := addcontext(&builder.Content, line); err != nil {
				return err
			}
			continue

		} else if command == "!note" {
			// used for comments
			continue

		} else if command == "!begin" {
			builder = Builder{}
			continue

		} else if command == "!end" {
			break

		} else if command == "!api" {
			api = strings.TrimSpace(args)
			continue

		} else if command == "!assistant" || command == "!user" {
			builder.New(command[1:])
			continue

		} else if len(command) > 2 && command[:2] == "!>" {
			key := command[2:]
			args = strings.TrimSpace(args)
			if args == "" {
				delete(headers, key)
			} else {
				headers[key] = args
			}
			continue

		} else if len(command) > 2 && command[:2] == "!:" {
			key := command[2:]
			args = strings.TrimSpace(args)
			if args == "" {
				delete(data, key)
			} else {
				var value any
				err := json.Unmarshal(([]byte)(args), &value)
				if err != nil {
					data[key] = args
				} else {
					data[key] = value
				}
			}
			continue
		}

		builder.Append(line)
	}

	if !strings.HasSuffix(api, "/") {
		api += "/"
	}

	if chat {
		api += "chat/completions"
		data["messages"] = builder.New("")
	} else {
		api += "completions"
		data["prompt"] = builder.New("")[0].Content
	}

	data["stream"] = true
	body, _ := json.Marshal(data)

	if debug {
		w := bufio.NewWriter(os.Stdout)
		fmt.Fprintf(w, "\n\nPOST %s HTTP/1.1\n", api)
		for key, value := range headers {
			fmt.Fprintf(w, "%s: %s\n", key, value)
		}
		fmt.Fprintf(w, "\n%s\n", body)
		return w.Flush()
	}

	req, err := http.NewRequest("POST", api, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		ebody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(ebody))
	}

	w := bufio.NewWriter(os.Stdout)
	if chat {
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

		if chat {
			var r ChatResponse
			json.Unmarshal(line, &r)
			w.WriteString(r.Choices[0].Delta.Content)
		} else {
			var r CompletionResponse
			json.Unmarshal(line, &r)
			w.WriteString(r.Content)
		}
		w.Flush()
	}
	if err := s.Err(); err != nil {
		return err
	}

	return w.Flush()
}

func run() error {
	profile := os.Getenv("ILLUME_PROFILE")
	if profile == "" {
		profile = DefaultProfile
	}

	var buf bytes.Buffer
	if lines, ok := Profiles[profile]; ok {
		for _, line := range lines {
			buf.WriteString(line)
			buf.WriteString("\n")
		}
	} else {
		f, err := os.Open(profile)
		if err != nil {
			return err
		}
		if _, err := io.Copy(&buf, f); err != nil {
			return err
		}
	}

	if _, err := io.Copy(&buf, os.Stdin); err != nil {
		return err
	}

	token := os.Getenv("ILLUME_TOKEN")
	return query(buf.String(), token)
}

func main() {
	if err := run(); err != nil {
		fmt.Printf("\n\n!error\n\n%s\n", err)
		os.Exit(1)
	}
}
