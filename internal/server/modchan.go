package server

// The mod's channel (docs/SPEC.md §8.6, Mods): a session with terminatr's
// mod gets a small HTTP listener on a Unix socket of its own, in its
// runtime directory, and the mod reports the session's state there with
// $.http.fetch (socketPath). HTTP because that is all a mod can speak to
// a socket: tm.sock's NDJSON needs a long-lived writer, and a mod's
// child process gets its stdin once, then closed. One socket per
// session, so the path says which session reports, and nothing else on
// the machine listens on a TCP port.

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/session"
)

// envModSocket names the mod's socket in the session's environment.
const envModSocket = "TERMINATR_MOD_SOCKET"

// modSocketName is the socket's name in the session's runtime dir.
const modSocketName = "mod.sock"

// maxModReport bounds a report's body.
const maxModReport = 4 << 10

// ModReport is the body of POST /v1/state: the state the mod holds, and
// the event that moved it there ("beat" for a heartbeat).
type ModReport struct {
	State  agent.State `json:"state"`
	Reason string      `json:"reason,omitempty"`
	Event  string      `json:"event,omitempty"`
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
	return mux
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
