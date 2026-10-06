package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// newUUID returns a random UUID v4.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// randID returns prefix plus n random lowercase hex digits.
func randID(prefix string, n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)[:n]
}

// writeAtomic writes data to path through a temp file and a rename.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// projectDirName is Claude's name for a cwd under ~/.claude/projects:
// every character but letters and digits becomes '-'.
func projectDirName(cwd string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, cwd)
}

// transcriptPath is the transcript of session sid started in cwd.
func transcriptPath(home, cwd, sid string) string {
	return filepath.Join(home, ".claude", "projects", projectDirName(cwd), sid+".jsonl")
}

// nowMS is the current time in Unix milliseconds.
func nowMS() int64 { return time.Now().UnixMilli() }

// stamp is a transcript timestamp.
func stamp() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// ensureTranscript creates the transcript file if it is missing.
func ensureTranscript(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// appendJSONL appends one JSON object as a line.
func appendJSONL(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// task is one entry of Claude's task list, as stored on disk.
type task struct {
	ID          string   `json:"id"`
	Subject     string   `json:"subject"`
	Description string   `json:"description"`
	ActiveForm  string   `json:"activeForm,omitempty"`
	Status      string   `json:"status"`
	Blocks      []string `json:"blocks"`
	BlockedBy   []string `json:"blockedBy"`
}

// taskDir is the task list directory of session sid.
func taskDir(home, sid string) string {
	return filepath.Join(home, ".claude", "tasks", sid)
}

// nextTaskID bumps and returns the session's task id high-water mark.
func nextTaskID(dir string) (string, error) {
	n := 0
	if b, err := os.ReadFile(filepath.Join(dir, ".highwatermark")); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	n++
	id := strconv.Itoa(n)
	return id, writeAtomic(filepath.Join(dir, ".highwatermark"), []byte(id), 0o644)
}

func readTask(dir, id string) (task, error) {
	var t task
	b, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return t, err
	}
	err = json.Unmarshal(b, &t)
	return t, err
}

func writeTask(dir string, t task) error {
	if t.Blocks == nil {
		t.Blocks = []string{}
	}
	if t.BlockedBy == nil {
		t.BlockedBy = []string{}
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, t.ID+".json"), b, 0o644)
}

// trustFile is ~/.claude.json.
func trustFile(home string) string { return filepath.Join(home, ".claude.json") }

func readTrustFile(home string) map[string]any {
	m := map[string]any{}
	if b, err := os.ReadFile(trustFile(home)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// isTrusted reports whether any of dirs, or one of their ancestors, has
// the trust dialog accepted.
func isTrusted(home string, dirs ...string) bool {
	projects, _ := readTrustFile(home)["projects"].(map[string]any)
	for _, dir := range dirs {
		for d := dir; ; d = filepath.Dir(d) {
			if p, ok := projects[d].(map[string]any); ok && p["hasTrustDialogAccepted"] == true {
				return true
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return false
}

func bypassAccepted(home string) bool {
	return readTrustFile(home)["bypassPermissionsModeAccepted"] == true
}

// updateTrustFile applies fn to ~/.claude.json, keeping other keys.
func updateTrustFile(home string, fn func(m map[string]any)) error {
	m := readTrustFile(home)
	fn(m)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(trustFile(home), b, 0o600)
}

func acceptTrust(home, cwd string) error {
	return updateTrustFile(home, func(m map[string]any) {
		projects, ok := m["projects"].(map[string]any)
		if !ok {
			projects = map[string]any{}
			m["projects"] = projects
		}
		p, ok := projects[cwd].(map[string]any)
		if !ok {
			p = map[string]any{}
			projects[cwd] = p
		}
		p["hasTrustDialogAccepted"] = true
	})
}

func acceptBypass(home string) error {
	return updateTrustFile(home, func(m map[string]any) {
		m["bypassPermissionsModeAccepted"] = true
	})
}

// settingsFile is the part of a --settings file the fake reads.
type settingsFile struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
	Hooks map[string][]hookGroup `json:"hooks"`
}

// readSettings reads a --settings value: a file, or inline JSON.
func readSettings(value string) (settingsFile, error) {
	var s settingsFile
	b := []byte(value)
	if !strings.HasPrefix(strings.TrimSpace(value), "{") {
		var err error
		if b, err = os.ReadFile(value); err != nil {
			return s, err
		}
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", value, err)
	}
	return s, nil
}

// denyDirs turns Edit(//abs/path/**) rules into directories.
func denyDirs(rules []string, home, cwd string) []string {
	var dirs []string
	for _, r := range rules {
		inner, ok := strings.CutPrefix(r, "Edit(")
		if !ok || !strings.HasSuffix(inner, ")") {
			continue
		}
		p := strings.TrimSuffix(inner, ")")
		p = strings.TrimSuffix(strings.TrimSuffix(p, "/**"), "/*")
		switch {
		case strings.HasPrefix(p, "//"):
			p = p[1:]
		case strings.HasPrefix(p, "~/"):
			p = filepath.Join(home, p[2:])
		case strings.HasPrefix(p, "/"):
		default:
			p = filepath.Join(cwd, p)
		}
		if p != "" {
			dirs = append(dirs, realPath(filepath.Clean(p)))
		}
	}
	return dirs
}

// realPath resolves symlinks in the longest existing prefix of p.
func realPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(realPath(parent), filepath.Base(p))
}

// denied reports whether path lies in one of dirs.
func denied(path string, dirs []string) bool {
	path = realPath(path)
	for _, d := range dirs {
		if path == d || strings.HasPrefix(path, strings.TrimSuffix(d, "/")+"/") {
			return true
		}
	}
	return false
}
