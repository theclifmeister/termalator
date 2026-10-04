package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
)

// listen opens the messaging socket, like Claude's uds-messaging one.
func (a *app) listen() {
	path := fmt.Sprintf("/tmp/fa-%d.sock", a.pid)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		a.log("error", map[string]any{"text": "messaging socket: " + err.Error()})
		return
	}
	_ = os.Chmod(path, 0o600)
	a.listener = ln
	a.sockPath = path
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go a.serveConn(c)
		}
	}()
}

// serveConn reads NDJSON lines; user messages are queued as prompts.
func (a *app) serveConn(c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var m struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "user" {
			continue
		}
		if text := contentText(m.Message.Content); text != "" {
			a.enqueue(job{kind: "prompt", text: text, via: "socket"})
		}
	}
}

// contentText reads a message content: a string or text blocks.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
