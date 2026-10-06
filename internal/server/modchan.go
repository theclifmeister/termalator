package server

// The mod's channel (docs/SPEC.md §8.6, Mods): a session with terminatr's
// mod gets a small HTTP listener on a Unix socket of its own, in its
// runtime directory, and the mod reports the session's state there with
// $.http.fetch (socketPath). HTTP because that is all a mod can speak to
// a socket: tm.sock's NDJSON needs a long-lived writer, and a mod's
// child process gets its stdin once, then closed. One socket per
// session, so the path says which session reports, and nothing else on
// the machine listens on a TCP port.
//
// The other way, the mod pulls: nothing can push into a mod. It long-polls
// GET /v1/prompts for the head of the session's prompt queue and acks it
// with POST /v1/prompts/{id}/ack (session/modprompt.go).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/session"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// envModSocket names the mod's socket in the session's environment.
const envModSocket = "TERMINATR_MOD_SOCKET"

// modSocketName is the socket's name in the session's runtime dir.
const modSocketName = "mod.sock"

// maxModReport bounds a report's body.
const maxModReport = 4 << 10

// Long poll bounds for GET /v1/prompts?wait=<seconds>.
const (
	modPollDefault = 20 * time.Second
	modPollMax     = 25 * time.Second
)

// ModAck is the body of POST /v1/prompts/{id}/ack.
type ModAck struct {
	Result string `json:"result"` // session.ModTaken, ModSubmitted or ModRefused
	Error  string `json:"error,omitempty"`
}

// ModReport is the body of POST /v1/state: the state the mod holds, and
// the event that moved it there ("beat" for a heartbeat).
type ModReport struct {
	State  agent.State `json:"state"`
	Reason string      `json:"reason,omitempty"`
	Event  string      `json:"event,omitempty"`
}

// ModUsage is the body of POST /v1/usage: what one turn used, numbers
// and the model's id only.
type ModUsage struct {
	Turn          string  `json:"turn,omitempty"`
	Model         string  `json:"model,omitempty"`
	Input         int64   `json:"input"`
	Output        int64   `json:"output"`
	CacheRead     int64   `json:"cache_read"`
	CacheCreation int64   `json:"cache_creation"`
	CostUSD       float64 `json:"cost_usd"`
}

// listenMod opens session id's mod socket in its runtime dir rt; s.mu
// held. Any listener the id had is closed first.
func (s *Server) listenMod(id, rt string) (string, error) {
	s.closeModLocked(id)
	path := filepath.Join(rt, modSocketName)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return "", err
	}
	os.Chmod(path, 0o600)
	srv := &http.Server{Handler: s.modHandler(id), ReadHeaderTimeout: 5 * time.Second}
	if s.mods == nil {
		s.mods = map[string]*http.Server{}
	}
	s.mods[id] = srv
	go srv.Serve(ln)
	return path, nil
}

// closeModLocked closes session id's mod listener, if any; s.mu held.
func (s *Server) closeModLocked(id string) {
	if srv := s.mods[id]; srv != nil {
		srv.Close()
		delete(s.mods, id)
	}
}

// closeMods closes every mod listener (server stop).
func (s *Server) closeMods() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.mods {
		s.closeModLocked(id)
	}
}

func (s *Server) modHandler(id string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/state", func(w http.ResponseWriter, r *http.Request) {
		var rep ModReport
		b, err := io.ReadAll(io.LimitReader(r.Body, maxModReport))
		if err == nil {
			err = json.Unmarshal(b, &rep)
		}
		if err == nil {
			err = checkModReport(rep)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		sess := s.sessions[id]
		s.mu.Unlock()
		if sess == nil {
			http.Error(w, "session "+id+" is gone", http.StatusGone)
			return
		}
		if err := sess.ModState(rep.State, rep.Reason, rep.Event); err != nil {
			code := http.StatusInternalServerError
			if errors.Is(err, session.ErrNoAgent) {
				code = http.StatusConflict
			}
			http.Error(w, err.Error(), code)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// What a turn used, added to its thread's totals.
	mux.HandleFunc("POST /v1/usage", func(w http.ResponseWriter, r *http.Request) {
		var u ModUsage
		b, err := io.ReadAll(io.LimitReader(r.Body, maxModReport))
		if err == nil {
			err = json.Unmarshal(b, &u)
		}
		if err == nil {
			err = checkModUsage(u)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s.modSession(w, id) == nil {
			return
		}
		s.addUsage(id, thread.Usage{Turns: 1, Input: u.Input, Output: u.Output,
			CacheRead: u.CacheRead, CacheCreation: u.CacheCreation, CostUSD: u.CostUSD})
		w.WriteHeader(http.StatusNoContent)
	})
	// The head of the prompt queue once the mod delivers it, or 204 after
	// the wait.
	mux.HandleFunc("GET /v1/prompts", func(w http.ResponseWriter, r *http.Request) {
		wait := modPollDefault
		if n, err := strconv.Atoi(r.URL.Query().Get("wait")); err == nil && n >= 0 {
			wait = min(time.Duration(n)*time.Second, modPollMax)
		}
		sess := s.modSession(w, id)
		if sess == nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), wait)
		defer cancel()
		p, ok, err := sess.NextModPrompt(ctx)
		if err != nil {
			modError(w, id, err)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
	})
	mux.HandleFunc("POST /v1/prompts/{pid}/ack", func(w http.ResponseWriter, r *http.Request) {
		var ack ModAck
		b, err := io.ReadAll(io.LimitReader(r.Body, maxModReport))
		if err == nil {
			err = json.Unmarshal(b, &ack)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sess := s.modSession(w, id)
		if sess == nil {
			return
		}
		if len(ack.Error) > 200 {
			ack.Error = ack.Error[:200]
		}
		if err := sess.AckModPrompt(r.PathValue("pid"), ack.Result, ack.Error); err != nil {
			modError(w, id, err)
			return
		}
		if ack.Result == session.ModSubmitted {
			s.watch.wake()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// The role's context, fresh (§7.8): the mod adds it to the
	// conversation's context, re-read after /clear and compaction. 204
	// for a session outside a project.
	mux.HandleFunc("GET /v1/context", func(w http.ResponseWriter, r *http.Request) {
		if s.modSession(w, id) == nil {
			return
		}
		b, err := s.contextOf(id)()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(b) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(b)
	})
	return mux
}

// modSession is session id, or nil after a 410 when it is gone.
func (s *Server) modSession(w http.ResponseWriter, id string) *session.Session {
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess == nil {
		http.Error(w, "session "+id+" is gone", http.StatusGone)
	}
	return sess
}

func modError(w http.ResponseWriter, id string, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, session.ErrNoAgent):
		code = http.StatusConflict
	case errors.Is(err, session.ErrExited):
		code = http.StatusGone
	case errors.Is(err, session.ErrNoModPrompt):
		code = http.StatusNotFound
	case errors.Is(err, session.ErrModAck):
		code = http.StatusBadRequest
	}
	http.Error(w, err.Error(), code)
}

func checkModReport(r ModReport) error {
	switch r.State {
	case agent.StateIdle, agent.StateWorking, agent.StateBlocked, agent.StateExited:
	default:
		return errors.New("state must be idle, working, blocked or exited")
	}
	if len(r.Reason) > 64 || len(r.Event) > 64 {
		return errors.New("reason and event are short words")
	}
	return nil
}

// maxModTokens bounds a count: a turn can't use more than a model reads.
const maxModTokens = 1 << 40

func checkModUsage(u ModUsage) error {
	for _, n := range []int64{u.Input, u.Output, u.CacheRead, u.CacheCreation} {
		if n < 0 || n > maxModTokens {
			return errors.New("token counts are small non-negative numbers")
		}
	}
	if u.CostUSD < 0 || u.CostUSD > 1e6 || u.CostUSD != u.CostUSD {
		return errors.New("cost_usd is a small non-negative number")
	}
	if len(u.Turn) > 128 || len(u.Model) > 64 {
		return errors.New("turn and model are short words")
	}
	return nil
}
