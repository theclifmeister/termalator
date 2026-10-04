package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/theclifmeister/termalator/internal/agent"
)

func init() {
	agent.RegisterGo("claude", func(m *agent.Manifest) agent.Agent {
		return &Agent{Agent: agent.FromManifest(m), m: m}
	})
}

// SocketField is the status-file field (StatusFile.Fields in the
// manifest) that holds Claude's messaging socket path.
const SocketField = "messaging_socket"

// dialTimeout bounds the probe and the delivery; the core falls back to
// paste on any error, so this must stay short.
const dialTimeout = 300 * time.Millisecond

// Agent is the manifest agent with the uds-messaging prompt channel.
type Agent struct {
	agent.Agent
	m *agent.Manifest
}

// Manifest exposes the manifest (agent.ManifestOf).
func (a *Agent) Manifest() *agent.Manifest { return a.m }

// Injector is the manifest's choice. The socket channel is opt-in
// (inject.prompt = "channel"): Claude 2.1.289 frames a message from the
// socket as coming from "another Claude session … not typed by your
// user", with caveats against treating it as the user's approval, which
// is wrong for a prompt the human sends. The core falls back to paste
// whenever Prompt fails.
func (a *Agent) Injector() agent.Injector { return a.Agent.Injector() }

// ErrNoSocket means the status file named no usable messaging socket.
var ErrNoSocket = errors.New("claude: no messaging socket")

// Prompt sends text to Claude's uds-messaging socket as one NDJSON line,
// {"type":"user","message":{"role":"user","content":text}}. Claude queues
// it correctly both idle and mid-turn (spike t-0004). It is used only for
// a tested version with a socket named in a trusted status file; the
// connect is the feature probe.
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
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
	if err != nil {
		return err
	}
	if _, err := c.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("claude: messaging socket: %w", err)
	}
	return nil
}
