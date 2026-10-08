package server

import (
	"path/filepath"
	"strings"

	"github.com/theclifmeister/terminatr/internal/agent"
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
	"TERMINATR_*", "HERDR_*",
}

// sessionEnv builds a session's environment from base (the server's own)
// plus set, the terminatr variables.
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

// withBinFirst returns base with the directory holding bin first on PATH,
// so a session's `tm` is the server's own (docs/SPEC.md §3.4). The rest of
// PATH stays as is; a later entry for the same directory is dropped, so a
// resume never lists it twice. Without a bin or a PATH, base is unchanged.
func withBinFirst(base []string, bin string) []string {
	if bin == "" {
		return base
	}
	dir := filepath.Dir(bin)
	out := make([]string, 0, len(base)+1)
	found := false
	for _, kv := range base {
		v, ok := strings.CutPrefix(kv, "PATH=")
		if !ok {
			out = append(out, kv)
			continue
		}
		found = true
		parts := []string{dir}
		for _, p := range filepath.SplitList(v) {
			if p != dir {
				parts = append(parts, p)
			}
		}
		out = append(out, "PATH="+strings.Join(parts, string(filepath.ListSeparator)))
	}
	if !found {
		out = append(out, "PATH="+dir)
	}
	return out
}
