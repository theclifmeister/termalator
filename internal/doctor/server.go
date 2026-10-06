package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/theclifmeister/terminatr/internal/keychain"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
)

// Live is what the server check learned about a running server.
type Live struct {
	Running bool
	// Sessions are the ids of the server's sessions; nil when unknown.
	Sessions map[string]bool
}

// lockHeld reports whether some process holds the server lock. It never
// creates the lock file.
func lockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return false, nil
}

// removeIfNoServer removes path while holding the server lock, so it can
// never remove a live server's socket or pid file.
func removeIfNoServer(p server.Paths, path string) error {
	f, err := os.OpenFile(p.Lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("a server started meanwhile; kept %s", path)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Server checks the run dir and the server. It never starts a server and
// never removes anything itself.
func Server(d Deps) ([]Check, Live) {
	const g = "server"
	p := d.Paths
	var out []Check
	live := Live{}
	if fi, err := os.Stat(p.RunDir); err == nil {
		if fi.Mode().Perm() != 0o700 {
			out = append(out, Check{Group: g, Name: "run dir", Status: Fail,
				Detail: fmt.Sprintf("%s has mode %o; the server needs 0700 (chmod 700 %s)", p.RunDir, fi.Mode().Perm(), p.RunDir)})
		} else {
			out = append(out, Check{Group: g, Name: "run dir", Status: OK, Detail: p.RunDir})
		}
	}
	held, err := lockHeld(p.Lock)
	if err != nil {
		return append(out, Check{Group: g, Name: "lock", Status: Fail, Detail: err.Error()}), live
	}
	if !held {
		out = append(out, Check{Group: g, Name: "server", Status: OK, Detail: "not running (any tm command starts it)"})
		for _, f := range []struct{ name, path string }{{"socket", p.Socket}, {"pid file", p.PID}} {
			if _, err := os.Lstat(f.path); err != nil {
				continue
			}
			path := f.path
			out = append(out, Check{Group: g, Name: "stale " + f.name, Status: Warn,
				Detail: path + " is left over from a server that is gone",
				Fix:    &Fix{Desc: "remove stale " + f.name + " " + path, Apply: func() error { return removeIfNoServer(p, path) }}})
		}
		live.Sessions = map[string]bool{}
		return append(out, runtimeDirs(p, live)...), live
	}
	live.Running = true
	pid := readPID(p.PID)
	c, err := server.Dial(p, proto.KindControl)
	if err != nil {
		var verr *proto.MismatchError
		if errors.As(err, &verr) {
			c := Check{Group: g, Name: "server", Status: Warn, Detail: fmt.Sprintf("pid %d: %s", pid, verr.Reason)}
			if d.Restart != nil {
				c.Fix = &Fix{Desc: fmt.Sprintf("restart the server (pid %d, protocol %d) with this tm; agents are resumed", pid, verr.Server.Protocol),
					Apply: d.Restart}
			}
			return append(out, c), live
		}
		return append(out, Check{Group: g, Name: "server", Status: Fail,
			Detail: fmt.Sprintf("holds the lock (pid %d) but doesn't answer (%v); see %s or run tm server stop --force", pid, err, p.Log)}), live
	}
	defer c.Close()
	var st proto.ServerStatus
	if err := c.Call(proto.MethodServerStatus, nil, &st); err != nil {
		return append(out, Check{Group: g, Name: "server", Status: Fail, Detail: fmt.Sprintf("pid %d: server.status: %v", pid, err)}), live
	}
	out = append(out, Check{Group: g, Name: "server", Status: OK,
		Detail: fmt.Sprintf("pid %d, up %s, protocol %d, %d sessions", st.PID, time.Since(st.Started).Round(time.Second), st.Protocol, st.Sessions)})
	if st.Build != d.Build {
		out = append(out, Check{Group: g, Name: "server build", Status: Warn,
			Detail: fmt.Sprintf("the server runs build %s, this tm is %s; tm server restart switches it (agents are resumed)", st.Build, d.Build)})
	}
	if d.GOOS == "darwin" {
		var ks proto.KeychainStatus
		err := c.Call(proto.MethodServerKeychain, nil, &ks)
		out = append(out, keychainCheck(d, ks, err)...)
	}
	if st.PreviousShutdown == "crash" {
		detail := "the previous server crashed"
		if len(st.Lost) > 0 {
			detail += "; lost sessions: " + strings.Join(st.Lost, " ")
		}
		out = append(out, Check{Group: g, Name: "last shutdown", Status: Warn, Detail: detail + "; see " + p.Log})
	}
	var list proto.SessionListResult
	if err := c.Call(proto.MethodSessionList, nil, &list); err == nil {
		live.Sessions = map[string]bool{}
		for _, s := range list.Sessions {
			live.Sessions[s.ID] = true
		}
		out = append(out, queueChecks(list.Sessions, time.Now())...)
		out = append(out, runtimeDirs(p, live)...)
	}
	return out, live
}

// queueChecks warns about sessions whose queued prompts are held while
// the agent is idle (a prompt box with text in it, a dialog): the prompt
// waits, and a coordinator's nudges with it (§7.5, §8.6).
func queueChecks(sessions []proto.SessionInfo, now time.Time) []Check {
	var out []Check
	for _, s := range sessions {
		n := s.QueueNote(now)
		if n == "" {
			continue
		}
		who := s.ID
		if s.Project != "" {
			who += " (" + s.Project + " " + s.Role
			if s.Thread != "" {
				who += " " + s.Thread
			}
			who += ")"
		}
		out = append(out, Check{Group: "server", Name: "prompt queue", Status: Warn,
			Detail: fmt.Sprintf("%s: %d queued prompt(s), %s; nothing is pasted until it clears, and a coordinator gets no nudges meanwhile. "+
				"Once it has been held for its bound the server drops it, or writes a tm prompt to the agent's socket, unconfirmed (journaled); tm agent explain %s shows the queue", who, s.Queued, n, s.ID)})
	}
	return out
}

// keychainCheck reports whether the server's sessions can reach the
// macOS login keychain (server.keychain). A restart is offered when it
// starts the server through launchd's GUI domain, or when this tm can
// reach the keychain itself: a direct restart from an SSH login, or from a
// session of the same server, would start the server where it was.
func keychainCheck(d Deps, ks proto.KeychainStatus, err error) []Check {
	const g, name = "server", "keychain"
	var perr *proto.Error
	switch {
	case errors.As(err, &perr) && perr.Code == proto.ErrUnknownMethod:
		return nil // an older server; the build check says to restart it
	case err != nil:
		return []Check{{Group: g, Name: name, Status: Warn, Detail: "couldn't ask the server: " + err.Error()}}
	case !ks.Checked:
		return nil
	case ks.OK:
		return []Check{{Group: g, Name: name, Status: OK, Detail: "sessions can reach the login keychain"}}
	}
	c := Check{Group: g, Name: name, Status: Warn,
		Detail: ks.Detail + "; gh and git push over https fail in its sessions: " + keychain.Fix}
	switch {
	case d.Restart == nil:
	case d.Launchd != nil && d.Launchd():
		c.Fix = &Fix{Desc: "restart the server in the desktop's session (through launchd), so its sessions can reach the keychain; agents are resumed",
			Apply: d.Restart}
	case d.Keychain != nil:
		if self := d.Keychain(); self.OK && !self.OverSSH {
			c.Fix = &Fix{Desc: "restart the server from this terminal, so its sessions can reach the keychain; agents are resumed",
				Apply: d.Restart}
		}
	}
	return []Check{c}
}

// runtimeDirs finds session runtime dirs (<run dir>/s/<id>) of sessions
// that no longer exist. Only called when the live session set is known.
func runtimeDirs(p server.Paths, live Live) []Check {
	root := filepath.Join(p.RunDir, "s")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Check
	for _, e := range entries {
		if !e.IsDir() || live.Sessions[e.Name()] {
			continue
		}
		dir := filepath.Join(root, e.Name())
		apply := func() error { return os.RemoveAll(dir) }
		if live.Running {
			// The server may have started that session since.
			id := e.Name()
			apply = func() error {
				if l := sessionsNow(p); l == nil || l[id] {
					return fmt.Errorf("kept %s: session %s may be running", dir, id)
				}
				return os.RemoveAll(dir)
			}
		}
		out = append(out, Check{Group: "server", Name: "runtime dir", Status: Warn,
			Detail: dir + " belongs to no running session",
			Fix:    &Fix{Desc: "remove " + dir, Apply: apply}})
	}
	return out
}

// sessionsNow asks the server for its sessions; nil if it can't.
func sessionsNow(p server.Paths) map[string]bool {
	c, err := server.Dial(p, proto.KindControl)
	if err != nil {
		if held, _ := lockHeld(p.Lock); !held {
			return map[string]bool{}
		}
		return nil
	}
	defer c.Close()
	var list proto.SessionListResult
	if err := c.Call(proto.MethodSessionList, nil, &list); err != nil {
		return nil
	}
	m := map[string]bool{}
	for _, s := range list.Sessions {
		m[s.ID] = true
	}
	return m
}

func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var pid int
	fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid)
	return pid
}
