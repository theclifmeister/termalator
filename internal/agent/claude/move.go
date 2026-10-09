package claude

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Moving a folder Claude has run in (tm project rename, docs/SPEC.md
// §5.1). Claude keeps a folder's conversations, and its auto memory,
// under <config dir>/projects/<the folder's path, every character but
// letters and digits made '-'>, and resumes a conversation only from the
// folder it was had in; its settings for the folder (trust, allowed
// tools) are ~/.claude.json's projects[<folder>]. MoveDir carries both
// over to the folder's new path.

// projectsDir is where Claude keeps conversations by folder.
func projectsDir(home string) string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	return filepath.Join(home, ".claude", "projects")
}

// folderKey is Claude's name for a folder under projectsDir.
func folderKey(dir string) string {
	b := []byte(dir)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

// MoveDir carries Claude's conversations and settings for the folder from
// over to the folder to, which the caller has moved there. Conversations
// already kept for to stay; from's settings entry is copied, not moved.
// A folder Claude never ran in is no error.
func (a *Agent) MoveDir(home, from, to string) error {
	// Claude names a folder by its real path (getcwd's), and tm by the
	// one it was given: both are carried over.
	pairs := [][2]string{{from, to}}
	if rf, rt := realish(from), realish(to); rf != from || rt != to {
		pairs = append(pairs, [2]string{rf, rt})
	}
	root := projectsDir(home)
	var errs []error
	for _, p := range pairs {
		if err := mergeDir(filepath.Join(root, folderKey(p[0])), filepath.Join(root, folderKey(p[1]))); err != nil {
			errs = append(errs, err)
		}
	}
	err := editProjects(home, func(projects map[string]json.RawMessage, _ string) (bool, error) {
		changed := false
		for _, p := range pairs {
			from, to := projectKey(p[0]), projectKey(p[1])
			raw, ok := projects[from]
			if _, taken := projects[to]; !ok || taken {
				continue
			}
			projects[to] = raw
			changed = true
		}
		return changed, nil
	})
	return errors.Join(append(errs, err)...)
}

// realish is path with symlinks resolved as far as it exists: a moved
// folder's old path is gone, but its parents' links (macOS's /var →
// /private/var) still resolve.
func realish(path string) string {
	rest := ""
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		if filepath.Dir(p) == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// mergeDir moves src to dst, or, when dst exists, each entry of src that
// dst lacks; src is removed when it ends up empty.
func mergeDir(src, dst string) error {
	if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
		return os.Rename(src, dst)
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if _, err := os.Lstat(d); err == nil {
			if e.IsDir() {
				errs = append(errs, mergeDir(s, d))
			}
			continue
		}
		errs = append(errs, os.Rename(s, d))
	}
	os.Remove(src) // only when empty
	return errors.Join(errs...)
}
