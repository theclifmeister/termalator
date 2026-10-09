package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/theclifmeister/terminatr/internal/agent"
)

func init() {
	agent.RegisterGo("codex", func(m *agent.Manifest) agent.Agent {
		return &Agent{Agent: agent.FromManifest(m), m: m}
	})
}

// HookCommand is the command of every hook tm gives Codex. It stays the
// same text across tm versions and sessions, so its trust hash does too:
// the shell expands TERMINATR_BIN, which the session sets.
const HookCommand = `"$TERMINATR_BIN" hook --agent codex`

// hookTimeout is a hook's timeout in seconds. Codex 0.160 clamps the
// SessionEnd and Interrupt hooks to 3 s and warns about a longer one.
func hookTimeout(event string) int {
	switch event {
	case "SessionEnd", "Interrupt":
		return 3
	}
	return 5
}

// stateSource is the source Codex names hooks from -c flags by in its
// trust keys (codex-rs hooks discovery, 0.160).
const stateSource = "/<session-flags>/config.toml"

// Agent is the manifest agent with the session's hooks added to every
// launch.
type Agent struct {
	agent.Agent
	m *agent.Manifest

	mu   sync.Mutex
	left map[string]string // tm session -> the thread a /clear left (Prompt)
}

// Manifest exposes the manifest (agent.ManifestOf).
func (a *Agent) Manifest() *agent.Manifest { return a.m }

// Launch is the manifest's launch plus two -c flags: the hooks, one
// command hook for every event the manifest maps, and hooks.state, which
// marks each as trusted for this session only. Without it Codex shows
// "Hooks need review" and runs none of them; nothing is written to the
// user's ~/.codex. They go before the kickoff, which ends the argv. A
// coordinator's sandbox profile stays only when it holds on this Codex
// (probe.go).
func (a *Agent) Launch(spec agent.LaunchSpec) (agent.Launch, error) {
	l, err := a.Agent.Launch(spec)
	if err != nil {
		return l, err
	}
	hooks, state, err := HookArgs(a.Events())
	if err != nil {
		return l, err
	}
	at := len(l.Argv)
	if l.Kickoff {
		at -= len(a.m.Launch.KickoffArgs)
	}
	extra := []string{"-c", hooks, "-c", state}
	l.Argv = append(l.Argv[:at:at], append(extra, l.Argv[at:]...)...)
	if spec.Role == "coordinator" {
		l = sandboxed(l, spec) // probe.go
	}
	return l, nil
}

// Events are the hook events the manifest names (Manifest.HookEvents).
func (a *Agent) Events() []string { return a.m.HookEvents() }

// HookArgs returns the values of the two -c flags for events:
//
//	hooks={Stop=[{hooks=[{type="command",command="…",timeout=5}]}],…}
//	hooks.state={"/<session-flags>/config.toml:stop:0:0"={trusted_hash="sha256:…"},…}
func HookArgs(events []string) (hooks, state string, err error) {
	var h, s []string
	for _, ev := range events {
		if !validEvent(ev) {
			return "", "", fmt.Errorf("codex: hook event %q is not one word", ev)
		}
		t := hookTimeout(ev)
		h = append(h, fmt.Sprintf(`%s=[{hooks=[{type="command",command=%s,timeout=%d}]}]`, ev, agent.TOMLString(HookCommand), t))
		sum, err := TrustHash(snake(ev), HookCommand, t)
		if err != nil {
			return "", "", err
		}
		key := fmt.Sprintf("%s:%s:0:0", stateSource, snake(ev))
		s = append(s, fmt.Sprintf(`%s={trusted_hash=%s}`, agent.TOMLString(key), agent.TOMLString(sum)))
	}
	return "hooks={" + strings.Join(h, ",") + "}", "hooks.state={" + strings.Join(s, ",") + "}", nil
}

// TrustHash is the hash Codex stores to trust one command hook group
// with one handler and no matcher: "sha256:" and the hex SHA-256 of the
// group's canonical JSON, keys sorted, no spaces (codex-rs
// hooks/src/engine/discovery.rs hook_hash, reproduced against 0.160).
func TrustHash(snakeEvent, command string, timeout int) (string, error) {
	type handler struct {
		Async   bool   `json:"async"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
		Type    string `json:"type"`
	}
	v := struct {
		EventName string    `json:"event_name"`
		Hooks     []handler `json:"hooks"`
	}{snakeEvent, []handler{{Command: command, Timeout: timeout, Type: "command"}}}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // serde_json doesn't escape <, > and &
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// snake turns an event name into Codex's snake case: PreToolUse ->
// pre_tool_use.
func snake(ev string) string {
	var b strings.Builder
	for i, r := range ev {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func validEvent(ev string) bool {
	if ev == "" {
		return false
	}
	for _, r := range ev {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}
