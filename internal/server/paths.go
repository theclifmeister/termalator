package server

import (
	"crypto/sha256"
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
//
// When the usual place would make the socket path too long, it falls back
// to /tmp/termalator-<uid>-<hash>, the hash naming TERMALATOR_HOME: every
// home keeps its own server, even with the fallback.
func RunDir(e Env) string {
	dir := filepath.Join(e.TermalatorHome, "run")
	if e.XDGRuntimeDir != "" {
		dir = filepath.Join(e.XDGRuntimeDir, "termalator")
	}
	if len(dir)+len("/tm.sock") > maxSocketPath {
		h := sha256.Sum256([]byte(resolve(e.TermalatorHome)))
		dir = fmt.Sprintf("/tmp/termalator-%d-%x", e.UID, h[:4])
	}
	return dir
}

// resolve returns path cleaned, with the symlinks in its longest existing
// prefix resolved: two spellings of one home (/var and /private/var on
// macOS) give the same result, and creating the home later doesn't change
// it.
func resolve(path string) string {
	path = filepath.Clean(path)
	var rest []string
	for p := path; ; p = filepath.Dir(p) {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{r}, rest...)...)
		}
		if p == filepath.Dir(p) {
			return path
		}
		rest = append([]string{filepath.Base(p)}, rest...)
	}
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
