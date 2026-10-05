package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Folder trust (docs/SPEC.md §8.6). Claude asks "Do you trust the files
// in this folder?" on its first start in a directory and records the
// answer in its global config, ~/.claude.json (or $CLAUDE_CONFIG_DIR/
// .claude.json), as projects[<dir>].hasTrustDialogAccepted. A thread's
// worktree is new every time, so tm records it there before the launch,
// as accepting the dialog once would; nothing else in the file changes.

// configPath is Claude's global config file for home.
func configPath(home string) string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	return filepath.Join(home, ".claude.json")
}

// lockWait bounds the wait for the config file's lock; a
// lock older than lockStale is left behind by a crash and ignored.
const (
	lockWait  = 2 * time.Second
	lockStale = 10 * time.Second
)

// TrustDir marks dir (and its real path, which Claude also looks up) as
// trusted in Claude's config, keeping every other key as it is.
func (a *Agent) TrustDir(home, dir string) error {
	dirs := []string{dir}
	if r, err := filepath.EvalSymlinks(dir); err == nil && r != dir {
		dirs = append(dirs, r)
	}
	path := configPath(home)
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r // a dotfiles symlink stays a symlink
	}
	unlock := lockConfig(path)
	defer unlock()

	top := map[string]json.RawMessage{}
	mode := fs.FileMode(0o600)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
		if len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, &top); err != nil {
				return fmt.Errorf("claude: %s: %w", path, err)
			}
		}
	}
	projects := map[string]json.RawMessage{}
	if raw, ok := top["projects"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &projects); err != nil {
			return fmt.Errorf("claude: %s: projects: %w", path, err)
		}
	}
	changed := false
	for _, d := range dirs {
		entry := map[string]json.RawMessage{}
		if raw, ok := projects[d]; ok && string(raw) != "null" {
			if err := json.Unmarshal(raw, &entry); err != nil {
				return fmt.Errorf("claude: %s: projects[%q]: %w", path, d, err)
			}
		}
		if string(entry["hasTrustDialogAccepted"]) == "true" {
			continue
		}
		entry["hasTrustDialogAccepted"] = json.RawMessage("true")
		b, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		projects[d] = b
		changed = true
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(projects)
	if err != nil {
		return err
	}
	top["projects"] = b
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, out, mode)
}

// lockConfig takes a lock directory next to the file (the proper-lockfile
// convention) for up to lockWait, so writers that honour it don't race
// this one. It is best effort: it returns the unlock, and when the lock
// can't be had the write goes ahead without it.
func lockConfig(path string) func() {
	lock := path + ".lock"
	deadline := time.Now().Add(lockWait)
	for {
		err := os.Mkdir(lock, 0o700)
		if err == nil {
			return func() { os.Remove(lock) }
		}
		if !errors.Is(err, fs.ErrExist) {
			return func() {}
		}
		if fi, err := os.Stat(lock); err == nil && time.Since(fi.ModTime()) > lockStale {
			return func() {}
		}
		if time.Now().After(deadline) {
			return func() {}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".claude.json.tm-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Chmod(mode)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
