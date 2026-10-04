package server

import (
	"fmt"
	"path/filepath"
)

// maxSocketPath keeps a margin under the sun_path limit (104 bytes on
// macOS, 108 on Linux, both including the trailing NUL).
const maxSocketPath = 100

// Env is the part of the environment the paths depend on.
type Env struct {
	TermalatorSocket string // $TERMALATOR_SOCKET
	TermalatorHome   string // $TERMALATOR_HOME, or ~/.termalator
	XDGRuntimeDir    string // $XDG_RUNTIME_DIR (Linux)
	UID              int
}

// RunDir is the short, per-user directory for every socket and lock: the
// server's socket, and any per-session sockets. It never lives under a
// project path, which can be long (docs/SPEC.md §3.2).
func RunDir(e Env) string {
	dir := filepath.Join(e.TermalatorHome, "run")
	if e.XDGRuntimeDir != "" {
		dir = filepath.Join(e.XDGRuntimeDir, "termalator")
	}
	if len(dir)+len("/tm.sock") > maxSocketPath {
		dir = fmt.Sprintf("/tmp/termalator-%d", e.UID)
	}
	return dir
}

// SocketPath returns the server's socket path.
func SocketPath(e Env) (string, error) {
	p := e.TermalatorSocket
	if p == "" {
		p = filepath.Join(RunDir(e), "tm.sock")
	}
	if len(p) > maxSocketPath {
		return "", fmt.Errorf("socket path %q is %d bytes; unix sockets allow at most %d here", p, len(p), maxSocketPath)
	}
	return p, nil
}
