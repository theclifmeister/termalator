package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
)

func init() {
	agent.RegisterGo("claude", func(m *agent.Manifest) agent.Agent {
		return &Agent{Agent: agent.FromManifest(m), m: m}
	})
}

// SocketField is the status-file field (StatusFile.Fields in the
// manifest) that holds Claude's messaging socket path.
const SocketField = "messaging_socket"

// dialTimeout bounds the delivery; the core falls back to paste on any
// error, so this must stay short.
const dialTimeout = 300 * time.Millisecond

// probeTimeout bounds Probe's connect, as Claude's own liveness check.
const probeTimeout = 250 * time.Millisecond

// Agent is the manifest agent with the uds-messaging prompt channel.
type Agent struct {
	agent.Agent
	m *agent.Manifest
}

// Manifest exposes the manifest (agent.ManifestOf).
func (a *Agent) Manifest() *agent.Manifest { return a.m }

// Injector is the manifest's choice, except that the socket never
// carries every prompt: inject.prompt = "channel" reads as paste. Claude
// frames a message from the socket as coming from "another Claude
// session … not typed by your user", with caveats against treating it as
// the user's approval, which is wrong for a prompt the human sends; it
// can't run slash commands, and it never says whether the message was
// delivered (2.1.291). The socket stays for the server's own fixed-word
// prompts once held (session.PromptOptions.Channel) and for Probe.
func (a *Agent) Injector() agent.Injector {
	if inj := a.Agent.Injector(); inj != agent.InjectChannel {
		return inj
	}
	return agent.InjectPaste
}

// ErrNoSocket means the status file named no usable messaging socket.
var ErrNoSocket = errors.New("claude: no messaging socket")

// Prompt sends text to Claude's uds-messaging socket as one NDJSON line,
// {"type":"user","message":{"role":"user","content":text}}. Claude queues
// it correctly both idle and mid-turn (spike t-0004). It is used only for
// a tested version with a socket named in a trusted status file; the
// connect is the feature probe.
//
// With a token (the CLAUDE_CODE_MESSAGING_TOKEN Claude gives its
// children, which tm's hooks report), an {"type":"auth","token":…} line
// goes first: Claude 2.1.291 may require it, and silently drops lines
// from a connection without it. Claude never answers on the sending
// connection, so nil means the lines were written, not that the message
// was delivered: it may still be held for approval or dropped.
func (a *Agent) Prompt(ctx context.Context, t agent.PromptTarget, text string) error {
	path := t.Fields[SocketField]
	if path == "" {
		return ErrNoSocket
	}
	if !a.m.Tested(t.Version) {
		return fmt.Errorf("claude: version %q: %w", t.Version, agent.ErrUntestedVersion)
	}
	d := net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("claude: messaging socket: %w", err)
	}
	defer c.Close()
	c.SetWriteDeadline(time.Now().Add(dialTimeout))
	msg, err := lines(t.Token, text)
	if err != nil {
		return err
	}
	// One write: the inbox reads whole lines, and the auth line must not
	// arrive alone.
	if _, err := c.Write(msg); err != nil {
		return fmt.Errorf("claude: messaging socket: %w", err)
	}
	return nil
}

// Probe is a connect-only liveness check of the messaging socket, the
// one Claude runs on its peers: it writes nothing (Claude closes a
// connection that sends no line), so it can't be mistaken for a message.
// No such socket or a refused connection means the process is gone.
func (a *Agent) Probe(ctx context.Context, t agent.PromptTarget) (agent.Liveness, error) {
	path := t.Fields[SocketField]
	if path == "" {
		return agent.LiveUnknown, ErrNoSocket
	}
	d := net.Dialer{Timeout: probeTimeout}
	c, err := d.DialContext(ctx, "unix", path)
	switch {
	case err == nil:
		c.Close()
		return agent.Live, nil
	case errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ECONNREFUSED):
		return agent.Gone, fmt.Errorf("messaging socket: %w", err)
	default:
		return agent.LiveUnknown, fmt.Errorf("messaging socket: %w", err)
	}
}

// lines is what Prompt writes: the auth line when there is a token, then
// the user line, each ending in a newline.
func lines(token, text string) ([]byte, error) {
	var out []byte
	if token != "" {
		auth, err := json.Marshal(map[string]any{"type": "auth", "token": token})
		if err != nil {
			return nil, err
		}
		out = append(auth, '\n')
	}
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
	if err != nil {
		return nil, err
	}
	return append(append(out, line...), '\n'), nil
}
