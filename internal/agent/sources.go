package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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
	// SessionStarts are the hook events that start a session or switch
	// to another. When set, any other event naming a session id that
	// isn't the current one is from a session the agent left (Codex
	// unloads the thread /clear left about a minute later, with
	// SessionEnd), and is ignored. Empty: every event may switch.
	SessionStarts []string `toml:"session_starts"`

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

// Version returns the agent version the file reports, or "".
func (f *StatusFile) Version(data []byte) string {
	if f.VersionField == "" {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal(data, &obj) != nil {
		return ""
	}
	v, _ := lookupString(obj, f.VersionField)
	return v
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
// rollout) and turns matching lines into signals, and into usage.
type JSONLTail struct {
	// PathField is the hook payload field that names the file, e.g.
	// Claude's transcript_path.
	PathField string     `toml:"path_field"`
	Rules     []TailRule `toml:"rules"`
	// Usage reads what the agent used from the lines that report it,
	// for an agent without a mod to report it (POST /v1/usage).
	Usage *TailUsage `toml:"usage"`
}

// TailRule matches one line.
type TailRule struct {
	Match      map[string]string `toml:"match"`
	TextField  string            `toml:"text_field"`  // string, or a list of {type:"text", text} blocks
	TextPrefix string            `toml:"text_prefix"` // optional
	State      State             `toml:"state"`
	Reason     string            `toml:"reason"`
}

// TailUsage is a manifest's [jsonl_tail.usage]: which lines report usage,
// and the dotted paths of their numbers. A path left empty reads as 0.
type TailUsage struct {
	Match         map[string]string `toml:"match"`
	Input         string            `toml:"input"`
	Output        string            `toml:"output"`
	CacheRead     string            `toml:"cache_read"`
	CacheCreation string            `toml:"cache_creation"`
	CostUSD       string            `toml:"cost_usd"`
	// PlanPercent is the plan limit used so far (0-100), for an agent on
	// a subscription that has no dollar cost (Codex on ChatGPT). Alone
	// it doesn't make a line report usage.
	PlanPercent string `toml:"plan_percent"`
	// Context is what the request read as context, ContextWindow the
	// model's window and Model its id, for the context use (§8.6).
	Context       string `toml:"context"`
	ContextWindow string `toml:"context_window"`
	Model         string `toml:"model"`
	// InputIncludesCache says input counts the cache reads too (OpenAI's
	// convention); they are taken out, so input is the uncached part.
	InputIncludesCache bool `toml:"input_includes_cache"`
	// Key is a path whose value tells one report from a repeat of it
	// (e.g. a running total): a line with the previous counted line's
	// key is not counted again.
	Key string `toml:"key"`
	// Turn matches the lines that end a turn, each counting one turn;
	// without it every usage line counts one.
	Turn map[string]string `toml:"turn"`
}

func (u *TailUsage) validate() error {
	if len(u.Match) == 0 {
		return errors.New("needs match")
	}
	if u.Input == "" && u.Output == "" && u.CacheRead == "" && u.CacheCreation == "" && u.CostUSD == "" && u.Context == "" {
		return errors.New("needs a path for input, output, cache_read, cache_creation, cost_usd or context")
	}
	if u.InputIncludesCache && (u.Input == "" || u.CacheRead == "") {
		return errors.New("input_includes_cache needs input and cache_read")
	}
	return nil
}

// Usage is what one line says the agent used: the same numbers as the
// mod's POST /v1/usage.
type Usage struct {
	Turns         int
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
	CostUSD       float64
	PlanPct       float64
	HasPlan       bool
	Context       int64
	ContextWindow int64
	Model         string
	Key           string // see TailUsage.Key
}

// TailLine is what one line of the file says: a signal, when a rule
// matched, and usage, when the line reports some.
type TailLine struct {
	Signal    Signal
	HasSignal bool
	Usage     *Usage
}

// Parse interprets one line. The first matching rule wins.
func (t *JSONLTail) Parse(line []byte) TailLine {
	var obj map[string]any
	if json.Unmarshal(line, &obj) != nil {
		return TailLine{}
	}
	var out TailLine
	for _, r := range t.Rules {
		if !matches(r.Match, obj) {
			continue
		}
		if r.TextPrefix != "" && !strings.HasPrefix(textOf(obj, r.TextField), r.TextPrefix) {
			continue
		}
		out.Signal, out.HasSignal = Signal{Source: "jsonl_tail", State: r.State, Reason: r.Reason}, true
		break
	}
	if t.Usage != nil {
		out.Usage = t.Usage.read(obj)
	}
	return out
}

// Line interprets one line's signal. The first matching rule wins.
func (t *JSONLTail) Line(line []byte) (Signal, bool) {
	l := t.Parse(line)
	return l.Signal, l.HasSignal
}

// read is the usage obj reports, or nil: a line that matches but has
// none of the numbers (Codex's first token_count has no info yet)
// reports none.
func (u *TailUsage) read(obj map[string]any) *Usage {
	turn := len(u.Turn) > 0 && matches(u.Turn, obj)
	if !matches(u.Match, obj) {
		if turn {
			return &Usage{Turns: 1}
		}
		return nil
	}
	var r Usage
	found := false
	num := func(path string) float64 {
		if path == "" {
			return 0
		}
		v, ok := lookup(obj, path)
		f, isNum := v.(float64)
		if !ok || !isNum || f < 0 || f != f || f > 1<<50 {
			return 0
		}
		found = true
		return f
	}
	r.Input, r.Output = int64(num(u.Input)), int64(num(u.Output))
	r.CacheRead, r.CacheCreation = int64(num(u.CacheRead)), int64(num(u.CacheCreation))
	r.CostUSD = num(u.CostUSD)
	r.Context = int64(num(u.Context))
	if !found {
		if turn {
			return &Usage{Turns: 1}
		}
		return nil
	}
	r.ContextWindow = int64(num(u.ContextWindow))
	if u.PlanPercent != "" {
		if v, ok := lookup(obj, u.PlanPercent); ok {
			if f, isNum := v.(float64); isNum && f >= 0 && f <= 100 {
				r.PlanPct, r.HasPlan = f, true
			}
		}
	}
	if u.InputIncludesCache {
		r.Input = max(0, r.Input-r.CacheRead)
	}
	if u.Model != "" {
		r.Model, _ = lookupString(obj, u.Model)
	}
	if u.Key != "" {
		r.Key, _ = lookupString(obj, u.Key)
	}
	if turn || len(u.Turn) == 0 {
		r.Turns = 1
	}
	return &r
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

// Heal merges a snapshot read into the mirrored list: the snapshot
// decides which items exist and their text and status, and the mirror
// keeps what the snapshot doesn't carry (the active text).
func (t *TodoSnapshot) Heal(mirror, snap []Todo) []Todo {
	active := map[string]string{}
	for _, m := range mirror {
		active[m.ID] = m.ActiveText
	}
	out := make([]Todo, len(snap))
	for i, s := range snap {
		if s.ActiveText == "" {
			s.ActiveText = active[s.ID]
		}
		out[i] = s
	}
	return out
}

// Foreign reports whether a hook event is from a session the agent left:
// it names a session id other than current, and isn't one of
// SessionStarts. It returns that id.
func (s Sources) Foreign(event string, payload map[string]any, current string) (string, bool) {
	if len(s.SessionStarts) == 0 || s.SessionField == "" || current == "" || slices.Contains(s.SessionStarts, event) {
		return "", false
	}
	sid, _ := lookupString(payload, s.SessionField)
	return sid, sid != "" && sid != current
}
