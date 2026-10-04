package server

// The ticker in the server (docs/SPEC.md §7.5, M7): it runs beside the
// sessions, is kicked by agent state changes, session exits and project
// commands, and acts through the server's own calls.

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/theclifmeister/termalator/internal/caller"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/ticker"
)

// Variables that shorten the ticker's intervals (tests).
const (
	envTickSweep = "TERMALATOR_TICK_SWEEP"
	envTickPR    = "TERMALATOR_TICK_PR"
	envTickNudge = "TERMALATOR_TICK_NUDGE"
	envTickDay   = "TERMALATOR_TICK_DAY" // the length of an auto-close day
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

func (h tickerHost) Prompt(id, text string) error {
	if _, perr := h.s.prompt(proto.SessionPromptParams{ID: id, Text: text}); perr != nil {
		return perr
	}
	return nil
}

func (h tickerHost) Alert(msg string) { h.s.alert(msg) }

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
		Day: envDuration(envTickDay),
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

// kick asks the ticker for a sweep soon.
func (s *Server) kick() {
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
