//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/theclifmeister/terminatr/internal/plat/ipc"
)

// listen opens the messaging socket, like Claude's uds-messaging one.
func (a *app) listen() {
	path := fmt.Sprintf("/tmp/fa-%d.sock", a.pid)
	os.Remove(path)
	ln, err := ipc.Listen(ipc.Addr(path))
	if err != nil {
		a.log("error", map[string]any{"text": "messaging socket: " + err.Error()})
		return
	}
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

// messagingToken is the token the fake gives its children, as Claude
// 2.1.291 does in CLAUDE_CODE_MESSAGING_TOKEN.
func (a *app) messagingToken() string { return fmt.Sprintf("fa-token-%d", a.pid) }

// serveConn reads NDJSON lines; user messages are queued as prompts. An
// {"type":"auth","token":…} line authenticates the connection; with
// FAKEAGENT_SOCKET_AUTH=required (Claude 2.1.291 on some platforms),
// user lines from a connection without it are dropped silently. Nothing
// is ever written back.
func (a *app) serveConn(c net.Conn) {
	defer c.Close()
	required := os.Getenv("FAKEAGENT_SOCKET_AUTH") == "required"
	authed := false
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var m struct {
			Type    string `json:"type"`
			Token   string `json:"token"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Type == "auth" {
			authed = m.Token == a.messagingToken()
			continue
		}
		if m.Type != "user" {
			continue
		}
		text := contentText(m.Message.Content)
		if required && !authed {
			a.log("socket-drop", map[string]any{"text": text, "why": "unauthenticated"})
			return
		}
		if text != "" {
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
