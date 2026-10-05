package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
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
		Argv0       []string `toml:"argv0"`        // basenames of argv[0] after unwrapping
		VersionArgs []string `toml:"version_args"` // e.g. ["--version"], for tm doctor
	} `toml:"identify"`

	Launch struct {
		Command     string            `toml:"command"`
		Args        []string          `toml:"args"`         // templates; empty results are dropped
		KickoffArgs []string          `toml:"kickoff_args"` // appended last when Kickoff is set; start with "--" if the CLI has variadic flags
		ResumeArgs  []string          `toml:"resume_args"`  // appended when Resume is set
		YoloArgs    []string          `toml:"yolo_args"`
		ModelArgs   []string          `toml:"model_args"` // appended when Model is set
		Env         map[string]string `toml:"env"`        // values are templates
		UnsetEnv    []string          `toml:"unset_env"`  // inherited variables to drop; trailing * = prefix
		Files       []ManifestFile    `toml:"files"`
	} `toml:"launch"`

	Inject struct {
		Prompt Injector `toml:"prompt"`
		// EmptyRule names a screen rule that matches only while the
		// prompt box is empty. The paste injector waits for it, so it
		// never appends to a restored or half-typed prompt.
		EmptyRule string `toml:"empty_rule"`
	} `toml:"inject"`

	// RemoteControl: reaching the session from another device, e.g.
	// Claude Code's Remote Control (docs/SPEC.md §8.2). Without args the
	// agent has none.
	RemoteControl RemoteControl `toml:"remote_control"`

	// Answer: how a question menu takes the user's answer, which the
	// coordinator relays with `tm thread answer` (docs/SPEC.md §11.2).
	// Without a rule the agent's menus are answered in its pane only.
	Answer Answer `toml:"answer"`

	Screen struct {
		// Resize: "follow" (the default) lets the console typed in size
		// the pane; "explicit" resizes it only on a window resize or a
		// split change, for inline renderers that garble their scrollback
		// on every resize (docs/SPEC.md §3.3).
		Resize Resize `toml:"resize"`
	} `toml:"screen"`

	// IgnoreFields: a hook event with any of these payload fields present
	// and non-empty (e.g. Claude's agent_id on subagent events) is ignored
	// for state, session id and todos. It still feeds [[hooks]] entries
	// that have a counter.
	IgnoreFields []string  `toml:"ignore_fields"`
	Hooks        []HookMap `toml:"hooks"`
	Todos        []TodoMap `toml:"todos"`
	Rules        []Rule    `toml:"rules"`

	// Sources holds tested_versions, session_field, [hook], [status_file],
	// [jsonl_tail] and [todos_snapshot].
	Sources
}

// RemoteControl is a manifest's [remote_control] table. Templates see the
// LaunchSpec; .RemoteName is the name the remote side lists the session
// under (the project's slug for a coordinator).
type RemoteControl struct {
	// Args are appended (before the kickoff) when it starts on.
	Args []string `toml:"args"`
	// Enable and Disable are in-session text, pasted as a prompt, that
	// turn it on or off in the running session (e.g. a slash command).
	// Empty: the session is restarted, resumed, with or without Args.
	Enable  string `toml:"enable"`
	Disable string `toml:"disable"`
	// DisableDialog answers a dialog the disable text opens.
	DisableDialog *RemoteDialog `toml:"disable_dialog"`
	// StatusField names a [status_file] fields entry that is non-empty
	// while remote control is on: the observed state, which wins over
	// what tm last asked for (an agent may reconnect on resume). Without
	// that entry (or a status file) tm goes by what it asked for.
	StatusField string `toml:"status_field"`
}

// Answer is a manifest's [answer] table. A menu's options are numbered;
// option N is chosen by typing its number. The option named TextOption
// takes the user's own words: its number focuses it, then the text is
// typed and Submit sent.
type Answer struct {
	// Rule names the screen rule that matches a question menu; tm
	// answers only while it is the screen's settled match.
	Rule       string `toml:"rule"`
	TextOption string `toml:"text_option"` // part of the free-text option's label
	Submit     string `toml:"submit"`      // keys after the text, e.g. "\r"
}

// AnswerOf returns a's [answer], or nil when it has none.
func AnswerOf(a Agent) *Answer {
	if m := ManifestOf(a); m != nil && m.Answer.Rule != "" {
		return &m.Answer
	}
	return nil
}

// RemoteDialog answers a dialog that in-session text opens: once the
// screen contains Contains, Keys are typed, and the change counts as made
// once the screen contains Done.
type RemoteDialog struct {
	Contains string `toml:"contains"`
	Keys     string `toml:"keys"`
	Done     string `toml:"done"`
}

// Supported reports whether the agent has remote control at all.
func (r *RemoteControl) Supported() bool { return r != nil && len(r.Args) > 0 }

// Text is the in-session text that turns it on or off, rendered for
// spec; "" means the session has to be restarted instead.
func (r *RemoteControl) Text(on bool, spec LaunchSpec) (string, error) {
	t := r.Disable
	if on {
		t = r.Enable
	}
	if t == "" {
		return "", nil
	}
	return render(t, spec)
}

// ObservesRemote reports whether the status file says if remote control
// is on (remote_control.status_field names one of its fields).
func (m *Manifest) ObservesRemote() bool {
	f := m.RemoteControl.StatusField
	return m.RemoteControl.Supported() && f != "" && m.StatusFile != nil && m.StatusFile.Fields[f] != ""
}

// RemoteControlOf returns a's [remote_control], or nil when a has none.
func RemoteControlOf(a Agent) *RemoteControl {
	if m := ManifestOf(a); m != nil && m.RemoteControl.Supported() {
		return &m.RemoteControl
	}
	return nil
}

// ManifestFile is a generated file written into the session's runtime dir
// before launch (a hook plugin, an extension, a settings file).
type ManifestFile struct {
	Path     string `toml:"path"`
	Template string `toml:"template"`
}

// HookMap maps one harness event to a signal. Every matching entry
// applies, so use negated matches ("!value") to keep entries exclusive.
type HookMap struct {
	Event     string            `toml:"event"`
	Match     map[string]string `toml:"match"` // see matches for the pattern syntax
	State     State             `toml:"state"` // empty: no state change
	Reason    string            `toml:"reason"`
	Transient bool              `toml:"transient"`
	// Counter is "+name" or "-name" (background activity started or
	// ended), keyed by the payload field CounterKey.
	Counter    string `toml:"counter"`
	CounterKey string `toml:"counter_key"`
	// Respond is a template printed back to the harness. It sees .Event,
	// .Payload and .Context (rendered only when the template uses it).
	Respond string `toml:"respond"`
}

// RendersAccess reports whether the manifest turns the access policy
// (LaunchSpec.Access) into the harness's own settings. `tm agent list`
// marks agents that don't as unenforced (docs/SPEC.md §8.7).
func (m *Manifest) RendersAccess() bool {
	for _, f := range m.Launch.Files {
		if strings.Contains(f.Template, ".Access") {
			return true
		}
	}
	for _, a := range m.Launch.Args {
		if strings.Contains(a, ".Access") {
			return true
		}
	}
	return false
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
	if m.Launch.Command != "" && strings.ContainsAny(m.Launch.Command, " \t") {
		errs = append(errs, errors.New("launch.command must be one program; put arguments in launch.args"))
	}
	switch m.Inject.Prompt {
	case "", InjectPaste, InjectNone, InjectChannel:
	default:
		errs = append(errs, fmt.Errorf("inject.prompt %q is not paste|channel|none", m.Inject.Prompt))
	}
	if rc := m.RemoteControl; !rc.Supported() && (rc.Enable != "" || rc.Disable != "") {
		errs = append(errs, errors.New("remote_control: enable and disable need args"))
	}
	if d := m.RemoteControl.DisableDialog; d != nil && (m.RemoteControl.Disable == "" || d.Contains == "" || d.Keys == "" || d.Done == "") {
		errs = append(errs, errors.New("remote_control.disable_dialog: needs disable, and contains, keys and done"))
	}
	switch m.Screen.Resize {
	case "", ResizeFollow, ResizeExplicit:
	default:
		errs = append(errs, fmt.Errorf("screen.resize %q is not follow|explicit", m.Screen.Resize))
	}
	valid := map[State]bool{"": true, StateIdle: true, StateWorking: true, StateBlocked: true, StateExited: true}
	for i, h := range m.Hooks {
		if h.Event == "" {
			errs = append(errs, fmt.Errorf("hooks[%d]: event is empty", i))
		}
		if !valid[h.State] {
			errs = append(errs, fmt.Errorf("hooks[%d]: unknown state %q", i, h.State))
		}
		if h.Counter != "" && (len(h.Counter) < 2 || (h.Counter[0] != '+' && h.Counter[0] != '-') || h.CounterKey == "") {
			errs = append(errs, fmt.Errorf("hooks[%d]: counter must be +name or -name, with counter_key", i))
		}
	}
	for i, t := range m.Todos {
		if err := t.validate(); err != nil {
			errs = append(errs, fmt.Errorf("todos[%d]: %w", i, err))
		}
	}
	if f := m.StatusFile; f != nil {
		if f.Path == "" || f.StateField == "" || len(f.StateMap) == 0 {
			errs = append(errs, errors.New("status_file: needs path, state_field and state_map"))
		}
		for k, v := range f.StateMap {
			if !valid[v] || v == "" {
				errs = append(errs, fmt.Errorf("status_file.state_map %q -> unknown state %q", k, v))
			}
		}
	}
	if t := m.JSONLTail; t != nil {
		if t.PathField == "" || len(t.Rules) == 0 {
			errs = append(errs, errors.New("jsonl_tail: needs path_field and rules"))
		}
		for i, r := range t.Rules {
			if !valid[r.State] || r.State == "" {
				errs = append(errs, fmt.Errorf("jsonl_tail.rules[%d]: unknown state %q", i, r.State))
			}
		}
	}
	if t := m.TodoSnapshot; t != nil && (t.Dir == "" || t.Glob == "" || t.ID == "" || t.Text == "" || t.Status == "") {
		errs = append(errs, errors.New("todos_snapshot: needs dir, glob, id, text and status"))
	}
	for i, r := range m.Rules {
		// A rule may also say "unknown": a screen on which no state can be
		// read (e.g. a transcript view), so the screen abstains.
		if r.ID == "" || r.State == "" || !(valid[r.State] || r.State == StateUnknown) {
			errs = append(errs, fmt.Errorf("rules[%d]: needs an id and a valid state", i))
		}
		if r.Regex != "" {
			if _, err := regexp.Compile(r.Regex); err != nil {
				errs = append(errs, fmt.Errorf("rules[%d] %s: %w", i, r.ID, err))
			}
		}
	}
	if a := m.Answer; a.Rule != "" || a.TextOption != "" || a.Submit != "" {
		found := false
		for _, x := range m.Rules {
			found = found || (x.ID == a.Rule && x.State == StateBlocked)
		}
		if !found {
			errs = append(errs, fmt.Errorf("answer.rule %q names no blocked rule", a.Rule))
		}
		if (a.TextOption == "") != (a.Submit == "") {
			errs = append(errs, errors.New("answer: text_option and submit go together"))
		}
	}
	if r := m.Inject.EmptyRule; r != "" {
		found := false
		for _, x := range m.Rules {
			found = found || x.ID == r
		}
		if !found {
			errs = append(errs, fmt.Errorf("inject.empty_rule %q names no rule", r))
		}
	}
	for _, f := range m.Launch.Files {
		if filepath.IsAbs(f.Path) || strings.Contains(f.Path, "..") {
			errs = append(errs, fmt.Errorf("launch.files %q must be relative, inside the runtime dir", f.Path))
		}
	}
	return errors.Join(errs...)
}

// Resize is when a pane running the agent is resized (screen.resize).
type Resize string

const (
	ResizeFollow   Resize = "follow"   // also to the console typed in
	ResizeExplicit Resize = "explicit" // only on a window resize or split change
)

// FollowsTyping reports whether typing into a console may resize a pane
// running a (nil: no agent, a shell).
func FollowsTyping(a Agent) bool {
	m := ManifestOf(a)
	return m == nil || m.Screen.Resize != ResizeExplicit
}

// manifestAgent implements Agent from a Manifest alone.
type manifestAgent struct{ m *Manifest }

// ManifestOf returns the manifest an agent was built from, or nil for an
// agent that has none.
func ManifestOf(a Agent) *Manifest {
	if x, ok := a.(interface{ Manifest() *Manifest }); ok {
		return x.Manifest()
	}
	return nil
}

func (a *manifestAgent) Manifest() *Manifest { return a.m }

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
	if spec.RemoteControl && !a.m.RemoteControl.Supported() {
		return out, fmt.Errorf("agent %s has no remote control", a.m.Name)
	}
	if spec.Resume && spec.AgentSID == "" {
		// Some CLIs open an interactive picker for an empty id.
		return out, fmt.Errorf("agent %s: resume needs the agent's session id", a.m.Name)
	}
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
		{spec.RemoteControl, a.m.RemoteControl.Args},
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
	out.Kickoff = spec.Kickoff != "" && !spec.Resume && len(l.KickoffArgs) > 0
	out.Unset = append([]string{}, l.UnsetEnv...)
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
	ignored := false
	for _, f := range a.m.IgnoreFields {
		if s, ok := lookupString(ev.Payload, f); ok && s != "" {
			ignored = true
			break
		}
	}
	var sigs []Signal
	var res HookResult
	base := Signal{Source: "hook", Seq: ev.Seq, At: ev.At}
	if !ignored && a.m.SessionField != "" {
		if sid, _ := lookupString(ev.Payload, a.m.SessionField); sid != "" {
			sig := base
			sig.AgentSID = sid
			sigs = append(sigs, sig)
		}
	}
	for _, h := range a.m.Hooks {
		if h.Event != ev.Event || !matches(h.Match, ev.Payload) {
			continue
		}
		if h.Counter != "" {
			sig := base
			sig.Counter = h.Counter
			sig.CounterKey, _ = lookupString(ev.Payload, h.CounterKey)
			sigs = append(sigs, sig)
		}
		if ignored {
			continue
		}
		if h.State != "" {
			sig := base
			sig.State, sig.Reason, sig.Transient = h.State, h.Reason, h.Transient
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
	if ignored {
		return sigs, res, nil
	}
	for _, tm := range a.m.Todos {
		if tm.Event != ev.Event || !matches(tm.Match, ev.Payload) {
			continue
		}
		ch, err := tm.change(ev.Payload)
		if err != nil {
			return sigs, res, fmt.Errorf("agent %s: todos on %s: %w", a.m.Name, ev.Event, err)
		}
		sig := base
		sig.Todo = &ch
		sigs = append(sigs, sig)
		break
	}
	return sigs, res, nil
}

func (a *manifestAgent) Sources() *Sources { return &a.m.Sources }

func (a *manifestAgent) Rules() []Rule { return a.m.Rules }

func (a *manifestAgent) Injector() Injector {
	if a.m.Inject.Prompt == "" {
		return InjectPaste
	}
	return a.m.Inject.Prompt
}

func (a *manifestAgent) Prompt(context.Context, PromptTarget, string) error {
	return fmt.Errorf("agent %s: no structured prompt channel; inject.prompt = %q", a.m.Name, a.Injector())
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
