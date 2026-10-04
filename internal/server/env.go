package server

import (
	"strings"

	"github.com/theclifmeister/termalator/internal/agent"
)

// Variables that leak the launching terminal's identity into a session
// (docs/SPEC.md §3.4). Agent-specific variables come from the manifests'
// unset_env lists (agent.BuiltinUnsetEnv).
var terminalEnv = []string{
	"TMUX", "TMUX_PANE", "STY", "WINDOW",
	"TERM_SESSION_ID", "WINDOWID", "ITERM_SESSION_ID",
	"KITTY_WINDOW_ID", "KITTY_PID", "WEZTERM_PANE", "ALACRITTY_WINDOW_ID",
	"WT_SESSION", "VTE_VERSION", "TERM_PROGRAM", "TERM_PROGRAM_VERSION",
	"ZELLIJ", "ZELLIJ_SESSION_NAME", "ZELLIJ_PANE_ID",
	"COLUMNS", "LINES",
	"TERMALATOR_*", "HERDR_*",
}

// sessionEnv builds a session's environment from base (the server's own)
// plus set, the termalator variables.
func sessionEnv(base []string, set map[string]string) []string {
	env := agent.FilterEnv(base, append(append([]string{}, terminalEnv...), agent.BuiltinUnsetEnv()...))
	out := env[:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; !ok {
			out = append(out, kv)
		}
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}
