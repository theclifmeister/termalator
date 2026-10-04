package server

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Paths are the files the server and its clients agree on (docs/SPEC.md
// §3.2, §5.1).
type Paths struct {
	Home     string // TERMALATOR_HOME, default ~/.termalator
	RunDir   string // the socket's directory; also holds the lock and pid file
	Socket   string
	Lock     string // RunDir/server.lock
	PID      string // RunDir/server.pid
	Log      string // Home/logs/server.log
	Sessions string // Home/state/sessions.json
}

// ResolvePaths computes the paths from the process environment.
//
// The lock and pid file sit next to the socket, so a TERMALATOR_SOCKET
// override (how tests isolate a server) also isolates its lock. A custom
// TERMALATOR_HOME ignores XDG_RUNTIME_DIR for the same reason: a test home
// must never share a run directory with the user's real server.
func ResolvePaths() (Paths, error) {
	home := os.Getenv("TERMALATOR_HOME")
	custom := home != ""
	if !custom {
		h, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("server: no home directory: %w", err)
		}
		home = filepath.Join(h, ".termalator")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return Paths{}, err
	}
	env := Env{TermalatorSocket: os.Getenv("TERMALATOR_SOCKET"), TermalatorHome: home, UID: os.Getuid()}
	if runtime.GOOS == "linux" && !custom {
		env.XDGRuntimeDir = os.Getenv("XDG_RUNTIME_DIR")
	}
	if env.TermalatorSocket != "" {
		if env.TermalatorSocket, err = filepath.Abs(env.TermalatorSocket); err != nil {
			return Paths{}, err
		}
	}
	sock, err := SocketPath(env)
	if err != nil {
		return Paths{}, err
	}
	run := filepath.Dir(sock)
	return Paths{
		Home:     home,
		RunDir:   run,
		Socket:   sock,
		Lock:     filepath.Join(run, "server.lock"),
		PID:      filepath.Join(run, "server.pid"),
		Log:      filepath.Join(home, "logs", "server.log"),
		Sessions: filepath.Join(home, "state", "sessions.json"),
	}, nil
}

// ensurePrivateDir creates dir (mode 0700) if needed and refuses a
// directory that is a symlink, belongs to someone else or is accessible to
// group or others.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("refusing to use %s: not a directory", dir)
	}
	if uid, ok := fileOwner(fi); ok && uid != os.Getuid() {
		return fmt.Errorf("refusing to use %s: owned by uid %d, not %d", dir, uid, os.Getuid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("refusing to use %s: mode %04o, want 0700 (chmod 700 it)", dir, fi.Mode().Perm())
	}
	return nil
}
