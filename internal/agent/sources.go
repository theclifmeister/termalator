package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Sources are the declarative state sources of an agent besides hooks and
// screen rules. The core runs them (watching files, tailing JSONL); this
// package only interprets what they read, so every agent gets the same
// behaviour from data (docs/SPEC.md §8.2).
type Sources struct {
	// TestedVersions are version prefixes (e.g. "2.1.") this manifest was
	// verified against. Undocumented sources are trusted only for these.
	TestedVersions []string `toml:"tested_versions"`

	// SessionField names the payload field carrying the agent's own
	// session id. It is read from every hook event; the latest wins.
	SessionField string `toml:"session_field"`

	Hook         HookTrim      `toml:"hook"`
	StatusFile   *StatusFile   `toml:"status_file"`
	JSONLTail    *JSONLTail    `toml:"jsonl_tail"`
	TodoSnapshot *TodoSnapshot `toml:"todos_snapshot"`
}

// SourceVars are the values path templates may use.
type SourceVars struct {
	Home     string
	PID      int
	AgentSID string
}

// ErrUntestedVersion means a status file reports an agent version outside
// TestedVersions. The core then ignores the file and labels the state as
// coming from the remaining sources.
var ErrUntestedVersion = errors.New("agent version not in tested_versions")

// Tested reports whether version matches one of the tested prefixes. An
// empty list accepts every version.
func (s *Sources) Tested(version string) bool {
	if len(s.TestedVersions) == 0 {
		return true
	}
	for _, p := range s.TestedVersions {
		if strings.HasPrefix(version, p) {
			return true
		}
	}
	return false
}

// HookTrim keeps hook payloads small before `tm hook` sends them to the
// server: only listed fields, long text cut to a prefix, and bulky fields
// only for the events that need them (e.g. todo tools).
type HookTrim struct {
	Keep     []string       `toml:"keep"`     // top-level fields to keep; empty keeps everything
	Truncate map[string]int `toml:"truncate"` // field -> max bytes
	KeepWhen []KeepWhen     `toml:"keep_when"`
}

// KeepWhen keeps extra fields when the payload matches.
type KeepWhen struct {
	Match  map[string]string `toml:"match"`
	Fields []string          `toml:"fields"`
}

// Trim returns the trimmed payload. hook_event_name is always kept.
func (h HookTrim) Trim(payload map[string]any) map[string]any {
	if len(h.Keep) == 0 && len(h.Truncate) == 0 && len(h.KeepWhen) == 0 {
		return payload
	}
	out := map[string]any{}
	keep := append([]string{"hook_event_name"}, h.Keep...)
	if len(h.Keep) == 0 {
		keep = nil
		for k := range payload {
			keep = append(keep, k)
		}
	}
	for _, kw := range h.KeepWhen {
		if matches(kw.Match, payload) {
			keep = append(keep, kw.Fields...)
		}
	}
	for _, k := range keep {
		if v, ok := payload[k]; ok {
			out[k] = v
		}
	}
	for k, n := range h.Truncate {
		if s, ok := out[k].(string); ok && len(s) > n {
			out[k] = truncateUTF8(s, n)
		}
	}
	return out
}

func truncateUTF8(s string, n int) string {
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// StatusFile is a JSON file the agent itself keeps current with its state,
// such as Claude Code's ~/.claude/sessions/<pid>.json.
type StatusFile struct {
	Path         string            `toml:"path"` // template over SourceVars
	StateField   string            `toml:"state_field"`
	StateMap     map[string]State  `toml:"state_map"`
	ReasonField  string            `toml:"reason_field"`
	ReasonMap    map[string]string `toml:"reason_map"`
	SessionField string            `toml:"session_field"`
	VersionField string            `toml:"version_field"`
	// Fields names extra values to expose, e.g. a messaging socket path
	// that a Go injector reads.
	Fields map[string]string `toml:"fields"`
}

// StatusReading is what one read of a status file yields.
type StatusReading struct {
	Signal Signal
	Fields map[string]string
}

// PathFor renders the file path.
func (f *StatusFile) PathFor(v SourceVars) (string, error) { return render(f.Path, v) }

// Read interprets the file's contents. It fails if the file is not valid,
// has no known state, or reports an untested version.
func (f *StatusFile) Read(data []byte, s *Sources) (StatusReading, error) {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return StatusReading{}, err
	}
	if f.VersionField != "" {
		v, _ := lookupString(obj, f.VersionField)
		if !s.Tested(v) {
			return StatusReading{}, fmt.Errorf("%w: %q", ErrUntestedVersion, v)
		}
	}
	raw, _ := lookupString(obj, f.StateField)
	st, ok := f.StateMap[raw]
	if !ok {
		return StatusReading{}, fmt.Errorf("status file: unknown %s %q", f.StateField, raw)
	}
	sig := Signal{Source: "status_file", State: st}
	if f.ReasonField != "" {
		r, _ := lookupString(obj, f.ReasonField)
		if mapped, ok := f.ReasonMap[r]; ok {
			r = mapped
		}
		sig.Reason = r
	}
	if f.SessionField != "" {
		sig.AgentSID, _ = lookupString(obj, f.SessionField)
	}
	fields := map[string]string{}
	for name, path := range f.Fields {
		if v, ok := lookupString(obj, path); ok {
			fields[name] = v
		}
	}
	return StatusReading{Signal: sig, Fields: fields}, nil
}

// JSONLTail follows a JSONL file the agent appends to (a transcript or
// rollout) and turns matching lines into signals.
type JSONLTail struct {
	// PathField is the hook payload field that names the file, e.g.
	// Claude's transcript_path.
	PathField string     `toml:"path_field"`
	Rules     []TailRule `toml:"rules"`
}

// TailRule matches one line.
type TailRule struct {
	Match      map[string]string `toml:"match"`
	TextField  string            `toml:"text_field"`  // string, or a list of {type:"text", text} blocks
	TextPrefix string            `toml:"text_prefix"` // optional
	State      State             `toml:"state"`
	Reason     string            `toml:"reason"`
}

// Line interprets one line. The first matching rule wins.
func (t *JSONLTail) Line(line []byte) (Signal, bool) {
	var obj map[string]any
	if json.Unmarshal(line, &obj) != nil {
		return Signal{}, false
	}
	for _, r := range t.Rules {
		if !matches(r.Match, obj) {
			continue
		}
		if r.TextPrefix != "" && !strings.HasPrefix(textOf(obj, r.TextField), r.TextPrefix) {
			continue
		}
		return Signal{Source: "jsonl_tail", State: r.State, Reason: r.Reason}, true
	}
	return Signal{}, false
}

// textOf returns the text at path: a string, or the first text block of a
// content list.
func textOf(obj map[string]any, path string) string {
	v, ok := lookup(obj, path)
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case []any:
		for _, b := range x {
			if m, ok := b.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					return s
				}
			}
		}
	}
	return ""
}

// TodoSnapshot is a directory of JSON files, one per todo item, that the
// agent keeps itself. The core re-reads it on the listed events to heal the
// hook-built mirror.
type TodoSnapshot struct {
	Dir       string                `toml:"dir"` // template over SourceVars
	Glob      string                `toml:"glob"`
	ID        string                `toml:"id"`
	Text      string                `toml:"text"`
	Status    string                `toml:"status"`
	StatusMap map[string]TodoStatus `toml:"status_map"`
	On        []string              `toml:"on"` // hook events that trigger a re-read
}

// DirFor renders the directory path.
func (t *TodoSnapshot) DirFor(v SourceVars) (string, error) { return render(t.Dir, v) }

// Parse builds the list from file contents keyed by file name. Files that
// don't match Glob or don't parse are skipped. Items are ordered by ID,
// numerically when the IDs are numbers.
func (t *TodoSnapshot) Parse(files map[string][]byte) []Todo {
	var out []Todo
	for name, data := range files {
		if ok, _ := filepath.Match(t.Glob, name); !ok {
			continue
		}
		var obj map[string]any
		if json.Unmarshal(data, &obj) != nil {
			continue
		}
		id, _ := lookupString(obj, t.ID)
		text, _ := lookupString(obj, t.Text)
		raw, _ := lookupString(obj, t.Status)
		st := TodoStatus(raw)
		if m, ok := t.StatusMap[raw]; ok {
			st = m
		}
		if !validTodoStatus(st) {
			continue
		}
		out = append(out, Todo{ID: id, Text: text, Status: st})
	}
	sort.Slice(out, func(i, j int) bool {
		a, errA := strconv.Atoi(out[i].ID)
		b, errB := strconv.Atoi(out[j].ID)
		if errA == nil && errB == nil {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}
