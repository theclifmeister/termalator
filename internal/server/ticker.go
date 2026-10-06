package server

// The ticker in the server (docs/SPEC.md §7.5, M7): it runs beside the
// sessions, is kicked by agent state changes, session exits and project
// commands, and acts through the server's own calls.

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/session"
	"github.com/theclifmeister/terminatr/internal/ticker"
)

// Variables that shorten the ticker's intervals (tests).
const (
	envTickSweep = "TERMINATR_TICK_SWEEP"
	envTickPR    = "TERMINATR_TICK_PR"
	envTickNudge = "TERMINATR_TICK_NUDGE"
	envTickDay   = "TERMINATR_TICK_DAY" // the length of an auto-close day
	// The remote control enforcement's pace and grace.
	envTickRemote = "TERMINATR_TICK_REMOTE"
	envTickGrace  = "TERMINATR_TICK_REMOTE_GRACE"
	// How long a queued prompt may be held while the agent is idle
	// (session.AgentConfig.PromptHold).
	envPromptHold = "TERMINATR_PROMPT_HOLD"
)

func envDuration(k string) time.Duration {
	d, _ := time.ParseDuration(os.Getenv(k))
	return d
}

// tickerHost is the ticker's view of the server.
type tickerHost struct {
	s   *Server
	ctx context.Context
}

func (h tickerHost) Sessions() []proto.SessionInfo { return h.s.list().Sessions }

// Prompt sends the ticker's fixed-word prompts (nudges, PR follow-ups).
// One held too long may go through the agent's channel (§8.6).
func (h tickerHost) Prompt(id, text string) error {
	if _, perr := h.s.promptWith(proto.SessionPromptParams{ID: id, Text: text}, session.PromptOptions{Channel: true}); perr != nil {
		return perr
	}
	return nil
}

// PromptFresh is Prompt for a prompt that may go stale in the queue:
// refresh rebuilds its text at delivery, or says to drop it.
func (h tickerHost) PromptFresh(id, text string, refresh func() (string, bool)) error {
	if _, perr := h.s.promptWith(proto.SessionPromptParams{ID: id, Text: text}, session.PromptOptions{Channel: true, Refresh: refresh}); perr != nil {
		return perr
	}
	return nil
}

func (h tickerHost) Alert(msg string) { h.s.alert(msg) }

// Unstick pastes the queued prompt session id's mod holds while its agent
// idles (session.Session.Unstick).
func (h tickerHost) Unstick(id string) string {
	h.s.mu.Lock()
	sess := h.s.sessions[id]
	h.s.mu.Unlock()
	if sess == nil {
		return ""
	}
	return sess.Unstick()
}

// Clear pastes the agent's clear prompt ([inject] clear) into session
// id, the ticker's auto-clear: a plain prompt, never through the channel
// (which runs no slash command), dropped at delivery unless still.
func (h tickerHost) Clear(id string, still func() bool) error {
	h.s.mu.Lock()
	sess := h.s.sessions[id]
	h.s.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("no session %s", id)
	}
	text := agent.ClearTextOf(sess.Agent())
	if text == "" {
		return fmt.Errorf("its agent has no clear prompt ([inject] clear)")
	}
	_, perr := h.s.promptWith(proto.SessionPromptParams{ID: id, Text: text}, session.PromptOptions{
		Refresh: func() (string, bool) { return text, still() },
	})
	if perr != nil {
		return perr
	}
	return nil
}

func (h tickerHost) Remote(id string, on bool) (proto.SessionRemoteResult, error) {
	res, perr := h.s.remote(proto.SessionRemoteParams{ID: id, On: on})
	if perr != nil {
		return proto.SessionRemoteResult{}, perr
	}
	return res.(proto.SessionRemoteResult), nil
}

func (h tickerHost) Resolve(slug, id string) (string, error) {
	if h.s.opts.RunCLI == nil {
		return "", fmt.Errorf("this server runs no project commands")
	}
	// Resolve calls back into this server; once it stops, a call could
	// start a new one.
	if h.ctx.Err() != nil {
		return "", fmt.Errorf("server stopping")
	}
	res := h.s.opts.RunCLI(proto.CLIRunParams{Args: []string{"thread", "resolve", id, "--project", slug}, Cwd: "/"},
		caller.Caller{Kind: caller.Ticker})
	if res.Code != 0 {
		return res.Stdout + res.Stderr, fmt.Errorf("exit %d", res.Code)
	}
	return res.Stdout, nil
}

// startTicker runs the ticker until ctx ends, after raising the restart
// items of the previous server's projects. The returned channel closes
// when the ticker has stopped, its last sweep included.
func (s *Server) startTicker(ctx context.Context) <-chan struct{} {
	t := ticker.New(ticker.Options{
		Host: tickerHost{s, ctx}, Log: s.log, State: ticker.StatePath(s.opts.Paths.Sessions),
		Sweep: envDuration(envTickSweep), PRPoll: envDuration(envTickPR), Nudge: envDuration(envTickNudge),
		Day: envDuration(envTickDay), RemoteEvery: envDuration(envTickRemote), RemoteGrace: envDuration(envTickGrace),
	})
	s.mu.Lock()
	s.tick = t
	how := s.prevShut
	resumed, lost := map[string]int{}, map[string]int{}
	for _, id := range s.resumed {
		if slug := s.prevProject[id]; slug != "" {
			resumed[slug]++
		}
	}
	for _, id := range s.lost {
		if slug := s.prevProject[id]; slug != "" {
			lost[slug]++
		}
	}
	s.mu.Unlock()
	if how != "" {
		t.ServerRestarted(how, resumed, lost)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		t.Run(ctx)
	}()
	return done
}

// kick asks the ticker for a sweep soon, and the watches to look again
// (a project command has run).
func (s *Server) kick() {
	s.watch.wake()
	s.mu.Lock()
	t := s.tick
	s.mu.Unlock()
	if t != nil {
		t.Kick()
	}
}

// alert logs msg and bumps the alert count, on which every client rings
// its bell (docs/SPEC.md §4).
func (s *Server) alert(msg string) {
	s.alerts.Add(1)
	s.log.Printf("alert: %s", msg)
}
