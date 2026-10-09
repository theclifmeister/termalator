package server

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"github.com/theclifmeister/terminatr/internal/plat/ipc"
)

// Env is the part of the environment the paths depend on.
type Env struct {
	TerminatrSocket string // $TERMINATR_SOCKET
	TerminatrHome   string // $TERMINATR_HOME, or ~/.terminatr
	XDGRuntimeDir   string // $XDG_RUNTIME_DIR (Linux)
	UID             int
}

// RunDir is the short, per-user directory for every socket and lock: the
// server's socket, and any per-session sockets. It never lives under a
// project path, which can be long (docs/SPEC.md §3.2).
//
// When the usual place would make the socket path too long, it falls back
// to /tmp/terminatr-<uid>-<hash> (ipc.ShortDir: on Windows %TEMP%), the
// hash naming TERMINATR_HOME: every home keeps its own server, even with
// the fallback.
func RunDir(e Env) string {
	dir := filepath.Join(e.TerminatrHome, "run")
	if e.XDGRuntimeDir != "" {
		dir = filepath.Join(e.XDGRuntimeDir, "terminatr")
	}
	if !ipc.Fits(dir, ipc.ServerName) {
		h := sha256.Sum256([]byte(resolve(e.TerminatrHome)))
		dir = filepath.Join(ipc.ShortDir(), fmt.Sprintf("terminatr-%d-%x", e.UID, h[:4]))
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
	a, err := ipc.ServerAddr(RunDir(e), e.TerminatrSocket)
	return string(a), err
}
