package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/BurntSushi/toml"
)

// ManifestVersion is the manifest format this binary reads.
const ManifestVersion = 1

// Manifest is the declarative definition of an agent. The format is
// documented in docs/SPEC.md §8.2; manifests/claude.toml is the reference.
type Manifest struct {
	ManifestVersion int    `toml:"manifest_version"`
	Name            string `toml:"name"`
	Display         string `toml:"display"`

	Identify struct {
		Argv0 []string `toml:"argv0"` // basenames of argv[0] after unwrapping
	} `toml:"identify"`

	Launch struct {
		Command     string            `toml:"command"`
		Args        []string          `toml:"args"`         // templates; empty results are dropped
		KickoffArgs []string          `toml:"kickoff_args"` // appended when Kickoff is set
		ResumeArgs  []string          `toml:"resume_args"`  // appended when Resume is set
		YoloArgs    []string          `toml:"yolo_args"`
		ModelArgs   []string          `toml:"model_args"` // appended when Model is set
		Env         map[string]string `toml:"env"`        // values are templates
		Files       []ManifestFile    `toml:"files"`
	} `toml:"launch"`

	Inject struct {
		Prompt Injector `toml:"prompt"`
	} `toml:"inject"`

	// IgnoreFields drops a hook event when any of these payload fields is
	// present and non-empty (e.g. Claude's agent_id on subagent events).
	IgnoreFields []string  `toml:"ignore_fields"`
	Hooks        []HookMap `toml:"hooks"`
	Todos        []TodoMap `toml:"todos"`
	Rules        []Rule    `toml:"rules"`
}

// ManifestFile is a generated file written into the session's runtime dir
// before launch (a hook plugin, an extension, a settings file).
type ManifestFile struct {
	Path     string `toml:"path"`
	Template string `toml:"template"`
}

// HookMap maps one harness event to a signal.
type HookMap struct {
	Event     string            `toml:"event"`
	Match     map[string]string `toml:"match"` // payload field == value, all must hold
	State     State             `toml:"state"` // empty: no state change
	Reason    string            `toml:"reason"`
	Transient bool              `toml:"transient"`
	// SessionField names the payload field holding the agent's session id.
	SessionField string `toml:"session_field"`
	// Respond is a template printed back to the harness. It sees .Event,
	// .Payload and .Context (rendered only when the template uses it).
	Respond string `toml:"respond"`
}

// TodoMap says which hook event carries the agent's todo list and how to
// read it (docs/SPEC.md §8.2). Paths are dotted payload paths.
type TodoMap struct {
	Event  string            `toml:"event"`
	Match  map[string]string `toml:"match"`
	List   string            `toml:"list"`   // e.g. "tool_input.todos"
	Text   string            `toml:"text"`   // field of each item, e.g. "content"
	Status string            `toml:"status"` // field of each item, e.g. "status"
	// StatusMap maps the harness's status values to pending, in_progress
	// or completed. Values not listed must already be one of those.
	StatusMap map[string]TodoStatus `toml:"status_map"`
}

// ParseManifest decodes and validates one manifest.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	md, err := toml.Decode(string(data), &m)
	if err != nil {
		return nil, err
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return nil, fmt.Errorf("unknown keys: %v", undec)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	var errs []error
	if m.ManifestVersion != ManifestVersion {
		errs = append(errs, fmt.Errorf("manifest_version = %d, want %d", m.ManifestVersion, ManifestVersion))
	}
	if m.Name == "" {
		errs = append(errs, errors.New("name is empty"))
	}
	if m.Launch.Command == "" {
		errs = append(errs, errors.New("launch.command is empty"))
	}
	switch m.Inject.Prompt {
	case "", InjectPaste, InjectNone, InjectChannel:
	default:
		errs = append(errs, fmt.Errorf("inject.prompt %q is not paste|channel|none", m.Inject.Prompt))
	}
	valid := map[State]bool{"": true, StateIdle: true, StateWorking: true, StateBlocked: true, StateExited: true}
	for i, h := range m.Hooks {
		if h.Event == "" {
			errs = append(errs, fmt.Errorf("hooks[%d]: event is empty", i))
		}
		if !valid[h.State] {
			errs = append(errs, fmt.Errorf("hooks[%d]: unknown state %q", i, h.State))
		}
	}
	validTodo := map[TodoStatus]bool{TodoPending: true, TodoInProgress: true, TodoCompleted: true}
	for i, t := range m.Todos {
		if t.Event == "" || t.List == "" || t.Text == "" || t.Status == "" {
			errs = append(errs, fmt.Errorf("todos[%d]: needs event, list, text and status", i))
		}
		for k, v := range t.StatusMap {
			if !validTodo[v] {
				errs = append(errs, fmt.Errorf("todos[%d]: status_map %q -> %q is not pending|in_progress|completed", i, k, v))
			}
		}
	}
	for i, r := range m.Rules {
		if r.ID == "" || r.State == "" || !valid[r.State] {
			errs = append(errs, fmt.Errorf("rules[%d]: needs an id and a valid state", i))
		}
	}
	for _, f := range m.Launch.Files {
		if filepath.IsAbs(f.Path) || strings.Contains(f.Path, "..") {
			errs = append(errs, fmt.Errorf("launch.files %q must be relative, inside the runtime dir", f.Path))
		}
	}
	return errors.Join(errs...)
}

// manifestAgent implements Agent from a Manifest alone.
type manifestAgent struct{ m *Manifest }

// FromManifest returns an Agent driven entirely by m.
func FromManifest(m *Manifest) Agent { return &manifestAgent{m: m} }

func (a *manifestAgent) Name() string { return a.m.Name }

func (a *manifestAgent) Identify(p ProcessInfo) bool {
	if len(p.Argv) == 0 {
		return false
	}
	base := filepath.Base(p.Argv[0])
	for _, n := range a.m.Identify.Argv0 {
		if base == n {
			return true
		}
	}
	return false
}

func (a *manifestAgent) Launch(spec LaunchSpec) (Launch, error) {
	l := a.m.Launch
	var out Launch
	argv := []string{l.Command}
	add := func(tmpls []string) error {
		for _, t := range tmpls {
			s, err := render(t, spec)
			if err != nil {
				return err
			}
			if s != "" {
				argv = append(argv, s)
			}
		}
		return nil
	}
	steps := []struct {
		on    bool
		tmpls []string
	}{
		{true, l.Args},
		{spec.Resume, l.ResumeArgs},
		{spec.Yolo, l.YoloArgs},
		{spec.Model != "", l.ModelArgs},
		{spec.Kickoff != "" && !spec.Resume, l.KickoffArgs},
	}
	for _, s := range steps {
		if s.on {
			if err := add(s.tmpls); err != nil {
				return out, err
			}
		}
	}
	out.Argv = argv
	for k, v := range l.Env {
		s, err := render(v, spec)
		if err != nil {
			return out, err
		}
		out.Env = append(out.Env, k+"="+s)
	}
	out.Files = map[string][]byte{}
	for _, f := range l.Files {
		s, err := render(f.Template, spec)
		if err != nil {
			return out, fmt.Errorf("launch.files %s: %w", f.Path, err)
		}
		out.Files[f.Path] = []byte(s)
	}
	return out, nil
}

func (a *manifestAgent) Hook(ev HookEvent, ctxFn func() ([]byte, error)) ([]Signal, HookResult, error) {
	for _, f := range a.m.IgnoreFields {
		if v, ok := ev.Payload[f]; ok && v != nil && v != "" {
			return nil, HookResult{}, nil
		}
	}
	var sigs []Signal
	var res HookResult
	for _, h := range a.m.Hooks {
		if h.Event != ev.Event || !matches(h.Match, ev.Payload) {
			continue
		}
		sig := Signal{Source: "hook", State: h.State, Reason: h.Reason, Seq: ev.Seq, At: ev.At, Transient: h.Transient}
		if h.SessionField != "" {
			sig.AgentSID, _ = ev.Payload[h.SessionField].(string)
		}
		if sig.State != "" || sig.AgentSID != "" {
			sigs = append(sigs, sig)
		}
		if h.Respond != "" && res.Stdout == nil {
			out, err := renderRespond(h.Respond, ev, ctxFn)
			if err != nil {
				return sigs, res, err
			}
			res.Stdout = out
		}
	}
	for _, tm := range a.m.Todos {
		if tm.Event != ev.Event || !matches(tm.Match, ev.Payload) {
			continue
		}
		todos, err := tm.extract(ev.Payload)
		if err != nil {
			return sigs, res, fmt.Errorf("agent %s: todos on %s: %w", a.m.Name, ev.Event, err)
		}
		sigs = append(sigs, Signal{Source: "hook", Seq: ev.Seq, At: ev.At, Todos: &todos})
		break
	}
	return sigs, res, nil
}

// extract reads the todo list from a payload.
func (tm TodoMap) extract(payload map[string]any) ([]Todo, error) {
	raw, ok := lookup(payload, tm.List)
	if !ok {
		return nil, fmt.Errorf("no field %q", tm.List)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%q is not a list", tm.List)
	}
	todos := make([]Todo, 0, len(items))
	for i, it := range items {
		obj, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] is not an object", tm.List, i)
		}
		text, _ := lookup(obj, tm.Text)
		status, _ := lookup(obj, tm.Status)
		ts, _ := status.(string)
		st := TodoStatus(ts)
		if mapped, ok := tm.StatusMap[ts]; ok {
			st = mapped
		}
		switch st {
		case TodoPending, TodoInProgress, TodoCompleted:
		default:
			return nil, fmt.Errorf("%s[%d]: unknown status %q", tm.List, i, ts)
		}
		t, _ := text.(string)
		todos = append(todos, Todo{Text: t, Status: st})
	}
	return todos, nil
}

// lookup follows a dotted path through nested JSON objects.
func lookup(v map[string]any, path string) (any, bool) {
	var cur any = v
	for _, key := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[key]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func (a *manifestAgent) Rules() []Rule { return a.m.Rules }

func (a *manifestAgent) Injector() Injector {
	if a.m.Inject.Prompt == "" {
		return InjectPaste
	}
	return a.m.Inject.Prompt
}

func (a *manifestAgent) Prompt(context.Context, string, string) error {
	return fmt.Errorf("agent %s: no structured prompt channel; inject.prompt = %q", a.m.Name, a.Injector())
}

func matches(want map[string]string, payload map[string]any) bool {
	for k, v := range want {
		got, _ := payload[k].(string)
		if got != v {
			return false
		}
	}
	return true
}

var funcs = template.FuncMap{
	// json encodes a value as a JSON string literal or object.
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	// rules formats one permission rule per directory, e.g.
	// rules "Read(/%s/**)" .Access.Read -> ["Read(//home/u/p/**)"].
	"rules": func(format string, dirs []string) []string {
		out := make([]string, 0, len(dirs))
		for _, d := range dirs {
			out = append(out, fmt.Sprintf(format, d))
		}
		return out
	},
	// concat joins lists, for building one JSON array from several grants.
	"concat": func(lists ...[]string) []string {
		out := []string{}
		for _, l := range lists {
			out = append(out, l...)
		}
		return out
	},
}

func render(tmpl string, data any) (string, error) {
	t, err := template.New("").Funcs(funcs).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// renderRespond renders a hook response. .Context is a method so the
// (possibly expensive) context is rendered only if the template uses it.
func renderRespond(tmpl string, ev HookEvent, ctxFn func() ([]byte, error)) ([]byte, error) {
	s, err := render(tmpl, respondData{ev: ev, ctxFn: ctxFn})
	return []byte(s), err
}

type respondData struct {
	ev    HookEvent
	ctxFn func() ([]byte, error)
}

func (d respondData) Event() string           { return d.ev.Event }
func (d respondData) Payload() map[string]any { return d.ev.Payload }
func (d respondData) Context() (string, error) {
	if d.ctxFn == nil {
		return "", nil
	}
	b, err := d.ctxFn()
	return string(b), err
}
