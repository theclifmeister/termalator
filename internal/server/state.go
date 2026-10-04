package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// State is sessions.json: what the server knows about its sessions, kept
// on disk so that the next server can tell a crash from a clean stop and
// resume agent sessions (docs/SPEC.md §3.6).
type State struct {
	Version   int       `json:"version"`
	ServerPID int       `json:"server_pid"`
	Started   time.Time `json:"started"`
	// Shutdown is written as "clean" last thing by a server that stopped
	// normally. Any other value found at start means the server crashed.
	Shutdown string          `json:"shutdown,omitempty"`
	NextID   int             `json:"next_id"`
	Sessions []SessionRecord `json:"sessions"`
}

// SessionRecord is one session in sessions.json.
type SessionRecord struct {
	ID             string    `json:"id"`
	Role           string    `json:"role"`
	Project        string    `json:"project,omitempty"`
	Thread         string    `json:"thread,omitempty"`
	Agent          string    `json:"agent,omitempty"`
	AgentSessionID string    `json:"agent_session_id,omitempty"`
	Argv           []string  `json:"argv"`
	Cwd            string    `json:"cwd"`
	Model          string    `json:"model,omitempty"`
	Yolo           bool      `json:"yolo,omitempty"`
	Created        time.Time `json:"created"`
	// CleanExit is true when the session was stopped by a clean server
	// stop rather than lost in a crash.
	CleanExit bool `json:"clean_exit"`
}

const stateVersion = 1

func loadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// saveState writes the file atomically: a temp file, fsync, rename.
func saveState(path string, st *State) error {
	if st.Sessions == nil {
		st.Sessions = []SessionRecord{}
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sessions-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
