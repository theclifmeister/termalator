// Package legacy moves an install of v0.1.0, which was called Termalator,
// over to Termilator: its TERMALATOR_* variables, its ~/.termalator state
// directory, the agent files and login service it wrote. Remove it once
// no v0.1.0 install is left (docs/OPERATIONS.md, "Upgrading from
// Termalator").
package legacy

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/theclifmeister/termilator/internal/home"
	"github.com/theclifmeister/termilator/internal/server"
)

const (
	oldName      = "termalator"
	oldEnvPrefix = "TERMALATOR"
	newEnvPrefix = "TERMILATOR"
	oldDir       = ".termalator"
	newDir       = ".termilator"
	// OldLabel and OldUnit are the login service names of v0.1.0.
	OldLabel = "dev.termalator.server"
	OldUnit  = "termalator.service"
)

// Env copies every TERMALATOR and TERMALATOR_* variable to its
// TERMILATOR name, unless that is set already, so a shell or service set
// up for v0.1.0 keeps working. It changes the process environment.
func Env() {
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if k != oldEnvPrefix && !strings.HasPrefix(k, oldEnvPrefix+"_") {
			continue
		}
		nk := newEnvPrefix + strings.TrimPrefix(k, oldEnvPrefix)
		if _, set := os.LookupEnv(nk); !set {
			os.Setenv(nk, v)
		}
	}
}

// Homes returns ~/.termalator and ~/.termilator.
func Homes() (old, new string, err error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(h, oldDir), filepath.Join(h, newDir), nil
}

// Pending returns the old and the new state directory when the old one
// still has to be moved: TERMILATOR_HOME is unset, ~/.termilator doesn't
// exist and ~/.termalator is a directory.
func Pending() (old, new string, ok bool) {
	if os.Getenv(home.Env) != "" {
		return "", "", false
	}
	old, new, err := Homes()
	if err != nil {
		return "", "", false
	}
	if _, err := os.Lstat(new); !errors.Is(err, fs.ErrNotExist) {
		return "", "", false
	}
	fi, err := os.Lstat(old)
	if err != nil || !fi.IsDir() {
		return "", "", false
	}
	return old, new, true
}

// OldPaths are the paths a v0.1.0 server of the home old used, so this tm
// can find and stop it.
func OldPaths(old string) server.Paths {
	run := filepath.Join(old, "run")
	if x := os.Getenv("XDG_RUNTIME_DIR"); runtime.GOOS == "linux" && x != "" {
		run = filepath.Join(x, oldName)
	}
	if len(run)+len("/tm.sock") > 100 {
		r := old
		if rr, err := filepath.EvalSymlinks(old); err == nil {
			r = rr
		}
		h := sha256.Sum256([]byte(r))
		run = fmt.Sprintf("/tmp/%s-%d-%x", oldName, os.Getuid(), h[:4])
	}
	sock := filepath.Join(run, "tm.sock")
	if s := os.Getenv("TERMILATOR_SOCKET"); s != "" {
		sock = s
		run = filepath.Dir(s)
	}
	return server.Paths{
		Home: old, RunDir: run, Socket: sock,
		Lock:     filepath.Join(run, "server.lock"),
		PID:      filepath.Join(run, "server.pid"),
		Log:      filepath.Join(old, "logs", "server.log"),
		Sessions: filepath.Join(old, "state", "sessions.json"),
	}
}

// OldServer reports whether a server holds the lock of p, and its pid
// from the pid file (0 if unknown).
func OldServer(p server.Paths) (pid int, running bool) {
	f, err := os.OpenFile(p.Lock, os.O_RDWR, 0)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		b, _ := os.ReadFile(p.PID)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return pid, true
	}
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return 0, false
}

// Migrate moves the state directory old to new and repairs what named the
// old path: the absolute paths in the state, project and generated agent
// files, the thread worktrees' git links, and Claude Code's per-directory
// transcript folders (so coordinators and threads resume their
// conversations). The caller makes sure no server uses old. Problems
// after the move are printed to w and don't fail it.
func Migrate(old, new string, w io.Writer) error {
	olds := []string{old}
	if r, err := filepath.EvalSymlinks(old); err == nil && r != old {
		olds = append(olds, r)
	}
	if err := os.Rename(old, new); err != nil {
		if _, _, ok := Pending(); !ok {
			return nil // another tm moved it a moment ago
		}
		return err
	}
	warn := func(format string, a ...any) { fmt.Fprintf(w, "tm: moving to %s: "+format+"\n", append([]any{new}, a...)...) }
	err := filepath.WalkDir(new, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch rel, _ := filepath.Rel(new, path); rel {
			case "worktrees", "server-bin", "logs":
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		generated := strings.Contains(path, string(filepath.Separator)+"run"+string(filepath.Separator)+"s"+string(filepath.Separator))
		if err := rewriteFile(path, olds, new, generated); err != nil {
			warn("%v", err)
		}
		return nil
	})
	if err != nil {
		warn("%v", err)
	}
	repairWorktrees(new, warn)
	moveClaudeProjects(olds, new, warn)
	return nil
}

// pathRE matches one spelling of the old home as a whole path, not as
// the prefix of a longer name (~/.termalator-spike).
func pathRE(old string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(old) + `($|[^A-Za-z0-9._-])`)
}

// Rewrite returns data with every old home path replaced by new and, when
// names is set, the old product names replaced by the new ones (the
// generated agent files carry TERMALATOR_* and the hook plugin's name).
// Paths are matched as whole paths, so ~/.termalator-spike stays.
func Rewrite(data []byte, olds []string, new string, names bool) []byte {
	for _, o := range olds {
		data = pathRE(o).ReplaceAll(data, []byte(strings.ReplaceAll(new, "$", "$$")+"${1}"))
	}
	if names {
		for _, r := range oldNames {
			data = bytes.ReplaceAll(data, []byte(r[0]), []byte(r[1]))
		}
	}
	return data
}

// oldNames are the names v0.1.0 wrote into generated agent files. Plain
// "termalator" is left alone: it may be a project's slug.
var oldNames = [][2]string{
	{"TERMALATOR", "TERMILATOR"},
	{`"name": "termalator", "description": "termalator session hooks"`, `"name": "termilator", "description": "termilator session hooks"`},
}

// rewriteFile rewrites one text file in place, keeping its mode.
func rewriteFile(path string, olds []string, new string, names bool) error {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() > 8<<20 {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil
	}
	out := Rewrite(data, olds, new, names)
	if bytes.Equal(out, data) {
		return nil
	}
	tmp := path + ".tm-rename"
	if err := os.WriteFile(tmp, out, fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Stale lists the regular files under dir that still name the old home
// old or the old names, such as the agent files a v0.1.0 server
// generated.
func Stale(dir, old string) []string {
	var out []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		stale := pathRE(old).Match(data)
		for _, r := range oldNames {
			stale = stale || bytes.Contains(data, []byte(r[0]))
		}
		if stale {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// Fix rewrites files that Stale found: the old home's paths become the
// new home's, and the old names the new ones.
func Fix(files []string) error {
	old, new, err := Homes()
	if err != nil {
		return err
	}
	if h := os.Getenv(home.Env); h != "" {
		new = h
	}
	var errs []error
	for _, f := range files {
		errs = append(errs, rewriteFile(f, []string{old}, new, true))
	}
	return errors.Join(errs...)
}

// repairWorktrees reconnects each moved thread worktree to its repository:
// the repository's admin files still name the old path.
func repairWorktrees(new string, warn func(string, ...any)) {
	dirs, _ := filepath.Glob(filepath.Join(new, "worktrees", "*", "*"))
	for _, d := range dirs {
		if fi, err := os.Lstat(filepath.Join(d, ".git")); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if out, err := exec.Command("git", "-C", d, "worktree", "repair").CombinedOutput(); err != nil {
			warn("git worktree repair in %s: %v: %s", d, err, strings.TrimSpace(string(out)))
		}
	}
}

// claudeKey is how Claude Code names a working directory's folder under
// ~/.claude/projects: every character but a letter or digit becomes "-".
var claudeKeyRE = regexp.MustCompile(`[^A-Za-z0-9]`)

func claudeKey(path string) string { return claudeKeyRE.ReplaceAllString(path, "-") }

// moveClaudeProjects renames Claude Code's transcript folders of the
// sessions that ran under the old home (coordinators in projects/,
// threads in worktrees/): Claude finds a conversation to resume by its
// working directory, which is now under the new home.
func moveClaudeProjects(olds []string, new string, warn func(string, ...any)) {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir = filepath.Join(h, ".claude")
	}
	dir = filepath.Join(dir, "projects")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	nk := claudeKey(new)
	for _, e := range entries {
		for _, o := range olds {
			ok := claudeKey(o)
			rest, found := strings.CutPrefix(e.Name(), ok)
			if !found || !(rest == "" || strings.HasPrefix(rest, "-projects") || strings.HasPrefix(rest, "-worktrees")) {
				continue
			}
			to := filepath.Join(dir, nk+rest)
			if _, err := os.Lstat(to); err == nil {
				warn("%s exists; left %s as it is", to, e.Name())
				break
			}
			if err := os.Rename(filepath.Join(dir, e.Name()), to); err != nil {
				warn("%v", err)
			}
			break
		}
	}
}

// OldServiceFile is the v0.1.0 login service file, if it is installed.
func OldServiceFile(goos, userHome string) (string, bool) {
	var p string
	switch goos {
	case "darwin":
		p = filepath.Join(userHome, "Library", "LaunchAgents", OldLabel+".plist")
	case "linux":
		p = filepath.Join(userHome, ".config", "systemd", "user", OldUnit)
	default:
		return "", false
	}
	_, err := os.Stat(p)
	return p, err == nil
}

// RemoveOldService unloads and removes the v0.1.0 login service. run runs
// a service manager command (launchctl, systemctl).
func RemoveOldService(goos, path string, run func(name string, args ...string) error) error {
	switch goos {
	case "darwin":
		run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), OldLabel))
	case "linux":
		run("systemctl", "--user", "disable", "--now", OldUnit)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if goos == "linux" {
		return run("systemctl", "--user", "daemon-reload")
	}
	return nil
}
