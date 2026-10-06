package server

import (
	"strings"

	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/proto"
)

// Context use (docs/SPEC.md §8.6, Context): the mod sends how many
// tokens the session's last request read as context, and the window,
// with its usage; the
// server keeps the latest per session, in memory, and shows it with the
// session (SessionInfo.Context) and in the project watch.

// ctxUse is a session's latest context size and its model's window.
type ctxUse struct{ tokens, window int64 }

// Context windows: the standard one, and the long one a model id marks
// with "[1m]" or "-1m".
const (
	windowStandard int64 = 200_000
	windowLong     int64 = 1_000_000
)

// contextWindow is the window of model; a context past the standard one
// proves the long one whatever the id said.
func contextWindow(model string, tokens int64) int64 {
	if strings.Contains(model, "[1m]") || strings.Contains(model, "-1m") || tokens > windowStandard {
		return windowLong
	}
	return windowStandard
}

// setContext records that session id's latest request read tokens as
// context, in a window of window tokens as the agent gives it (0: judged
// by the model's id); 0 tokens leaves what is kept.
func (s *Server) setContext(id, model string, tokens, window int64) {
	if tokens <= 0 {
		return
	}
	if model == "" {
		s.mu.Lock()
		model = s.records[id].Model
		s.mu.Unlock()
	}
	if window < tokens {
		window = contextWindow(model, tokens)
	}
	now := ctxUse{tokens, window}
	was, had := s.ctxOf.Swap(id, now)
	s.mu.Lock()
	coord := s.records[id].Role == proto.RoleCoordinator
	s.mu.Unlock()
	if coord && crossedContext(was, had, now) {
		s.alerts.Add(1) // the consoles ring their bell
	}
}

// crossedContext reports whether now is at or past [ui] context_hint
// where the use before (was, when had) was below it: one alert per
// crossing, none while it stays above.
func crossedContext(was any, had bool, now ctxUse) bool {
	hint := config.DefaultContextHint
	if cfg, err := config.Load(); err == nil {
		hint = cfg.ContextHint
	}
	if hint <= 0 || now.tokens*100 < now.window*int64(hint) {
		return false
	}
	if !had {
		return true
	}
	w := was.(ctxUse)
	return w.tokens*100 < w.window*int64(hint)
}
