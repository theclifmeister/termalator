package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"github.com/BurntSushi/toml"

	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/guard"
)

// ManifestVersion is the manifest format this binary reads.
const ManifestVersion = 1

// Manifest is the declarative definition of an agent. The format is
// documented in docs/SPEC.md §8.2; manifests/claude.toml is the reference.
type Manifest struct {
	ManifestVersion int    `toml:"manifest_version"`
	Name            string `toml:"name"`
	Display         string `toml:"display"`
	// RoleFiles are the names, besides AGENTS.md, under which the
	// coordinator's role file is linked in the project folder for this
	// agent to read (Claude Code: "CLAUDE.md").
	RoleFiles []string `toml:"role_files"`

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
		// TokenEnv names a variable of the hook's environment that tm hook
		// hands to the server with every event, for the prompt channel
		// (PromptTarget.Token): Claude's messaging token. The server
		// keeps it in memory only and never logs it.
		TokenEnv string `toml:"token_env"`
		// Clear is the prompt that clears the conversation (Claude's
		// /clear), pasted like any prompt: the ticker's auto-clear of an
		// idle coordinator (auto_clear) sends it. Empty: the agent has
		// none, and auto-clear leaves its sessions alone.
		Clear string `toml:"clear"`
	} `toml:"inject"`

	// RemoteControl: reaching the session from another device, e.g.
	// Claude Code's Remote Control (docs/SPEC.md §8.2). Without args the
	// agent has none.
	RemoteControl RemoteControl `toml:"remote_control"`

	// Answer: how a question menu takes the user's answer, which the
	// coordinator relays with `tm thread answer` (docs/SPEC.md §11.2).
	// Without a rule the agent's menus are answered in its pane only.
	Answer Answer `toml:"answer"`

	// Models are the models a thread of this agent may be started with
	// (tm thread start --model), each with a line on when it fits; none
	// means threads run the agent's default. They need model_args.
	Models []Model `toml:"models"`

	// Guard: the agent's own paths the guard knows (docs/SPEC.md §8.6).
	Guard Guard `toml:"guard"`

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
// typed and Submit sent. When the option needs a key to open its text
// field (Codex: Tab for notes), TextKey is sent between the two. Where a
// number submits the option at once (Codex), TextFocus moves to the text
// option instead: its key, typed N-1 times from the first option.
type Answer struct {
	// Rule names the screen rule that matches a question menu; tm
	// answers only while it is the screen's settled match.
	Rule       string `toml:"rule"`
	TextOption string `toml:"text_option"` // part of the free-text option's label
	TextFocus  string `toml:"text_focus"`  // key that moves down one option, in place of the number, e.g. "\x1b[B"
	TextKey    string `toml:"text_key"`    // keys after the option's number that open its text field, e.g. "\t"
	Submit     string `toml:"submit"`      // keys after the text, e.g. "\r"
}

// Guard is a manifest's [guard] table. Each entry is a path relative to
// home ("~/" optional) or under an environment variable ("$CODEX_HOME/auth.json").
type Guard struct {
	// Secrets are the agent's credential stores. The credentials rule
	// covers every known agent's, whatever the session's agent.
	Secrets []string `toml:"secrets"`
	// Writable are the agent's own folders its threads write to besides
	// the worktree and the temporary folders (the worktree-only rule).
	Writable []string `toml:"writable"`
	// Tools are the agent's tools the guard judges, by name: each one's
	// kind (shell, write, patch, read, glob) and the input fields that
	// kind reads, e.g. Bash = { kind = "shell", fields = ["command"] }.
	// A tool not listed is not judged.
	Tools map[string]guard.Tool `toml:"tools"`
}

// GuardPaths resolves [guard] entries to absolute paths, against home
// and getenv. An entry whose variable is unset or empty, or a relative
// one without a home, is left out.
func GuardPaths(entries []string, home string, getenv func(string) string) []string {
	var out []string
	for _, e := range entries {
		missing := false
		p := os.Expand(e, func(name string) string {
			v := getenv(name)
			if v == "" {
				missing = true
			}
			return v
		})
		p = strings.TrimPrefix(p, "~/")
		if missing || p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			if home == "" {
				continue
			}
			p = filepath.Join(home, p)
		}
		out = append(out, filepath.Clean(p))
	}
	return out
}

// validGuardPath reports whether a [guard] entry is home-relative or
// starts with a variable, and stays out of "..".
func validGuardPath(e string) bool {
	if e == "" || slices.Contains(strings.Split(filepath.ToSlash(e), "/"), "..") {
		return false
	}
	return !filepath.IsAbs(e) || strings.HasPrefix(e, "$")
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

// ClearTextOf is the prompt that clears a's conversation ([inject]
// clear), "" when it has none.
func ClearTextOf(a Agent) string {
	if m := ManifestOf(a); m != nil {
		return m.Inject.Clear
	}
	return ""
}

// Model is one [[models]] entry: a name the agent's model_args accept,
// and one line on when it fits, for the coordinator (tm context).
type Model struct {
	Name  string `toml:"name"`
	About string `toml:"about"`
	// Default marks the model a launch passes when none is chosen (at
	// most one); without one the agent runs the user's own default.
	Default bool `toml:"default"`
}

// FindModel returns the manifest's model named name.
func (m *Manifest) FindModel(name string) (Model, bool) {
	for _, x := range m.Models {
		if x.Name == name {
			return x, true
		}
	}
	return Model{}, false
}

// DefaultModel returns the name of the model marked default, "" for none.
func (m *Manifest) DefaultModel() string {
	for _, x := range m.Models {
		if x.Default {
			return x.Name
		}
	}
	return ""
}

// ModelsOf returns the [[models]] of a's manifest, nil for none: the
// catalog's fallback. What a thread may use is Models, which config.toml
// can replace.
func ModelsOf(a Agent) []Model {
	if m := ManifestOf(a); m != nil {
		return m.Models
	}
	return nil
}

// Models is agent a's models catalog (docs/SPEC.md §8.2, §11.2): the
// user's [agents.<name>] models in config.toml when set, else the
// manifest's [[models]]; Default marks the model a launch passes when
// none is chosen. cfg nil is the manifest's alone.
func Models(a Agent, cfg *config.Config) []Model {
	if a == nil {
		return nil
	}
	return MergeModels(ModelsOf(a), cfg.Agent(a.Name()))
}

// MergeModels lays the user's settings s over the manifest's models:
// s's models, when set, replace the list as a whole (so one can be
// removed); s's default_model, when set, is the default ("" none),
// else the manifest's while the list still has it. A default the list
// doesn't have is none (tm doctor reports it).
func MergeModels(manifest []Model, s config.AgentSettings) []Model {
	def := ""
	for _, x := range manifest {
		if x.Default {
			def = x.Name
		}
	}
	if s.HasDefault {
		def = s.DefaultModel
	}
	var out []Model
	if s.HasModels {
		for _, x := range s.Models {
			out = append(out, Model{Name: x.Name, About: x.About})
		}
	} else {
		for _, x := range manifest {
			out = append(out, Model{Name: x.Name, About: x.About})
		}
	}
	for i := range out {
		out[i].Default = out[i].Name == def
	}
	return out
}

// WithDefaultModel is spec with, when it chooses no model, the
// catalog's default (Models): the one config.toml names, or the
// manifest's; with none, AgentDefault, so the agent runs its own.
func WithDefaultModel(a Agent, cfg *config.Config, spec LaunchSpec) LaunchSpec {
	if spec.Model == "" {
		spec.Model = DefaultOf(Models(a, cfg))
		spec.AgentDefault = spec.Model == ""
	}
	return spec
}

// DefaultOf is the name of the model marked default, "" for none.
func DefaultOf(models []Model) string {
	for _, x := range models {
		if x.Default {
			return x.Name
		}
	}
	return ""
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
	// .Payload, .Context (rendered only when the template uses it) and
	// .Guard (the guard's refusal of the payload's tool call, or nil;
	// judged only when the template uses it).
	Respond string `toml:"respond"`
	// Timeout is how long tm hook waits for the response: "" (the
	// default, 500 ms) or "context" (3 s, for the event whose response
	// carries the session's context). See HookTimeout.
	Timeout string `toml:"timeout"`
}

// Hook timeouts a [[hooks]] entry may ask for.
const (
	HookTimeoutResponse = "response"
	HookTimeoutContext  = "context"
)

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
	seen := map[string]bool{}
	for i, x := range m.Models {
		switch {
		case config.CheckModelName(x.Name) != nil:
			errs = append(errs, fmt.Errorf("models[%d]: name %q is not one word of letters, digits and ._:/@[]-", i, x.Name))
		case seen[x.Name]:
			errs = append(errs, fmt.Errorf("models[%d]: %q is listed twice", i, x.Name))
		}
		seen[x.Name] = true
		if config.CheckModelAbout(x.About) != nil {
			errs = append(errs, fmt.Errorf("models[%d]: about must be one line of 1 to 120 characters", i))
		}
	}
	defaults := 0
	for _, x := range m.Models {
		if x.Default {
			defaults++
		}
	}
	if defaults > 1 {
		errs = append(errs, errors.New("models: at most one may be default"))
	}
	if len(m.Models) > 0 && len(m.Launch.ModelArgs) == 0 {
		errs = append(errs, errors.New("models need launch.model_args"))
	}
	for _, e := range m.Guard.Secrets {
		if !validGuardPath(e) {
			errs = append(errs, fmt.Errorf("guard.secrets: %q is not relative to home or under $VAR", e))
		}
	}
	for _, e := range m.Guard.Writable {
		if !validGuardPath(e) {
			errs = append(errs, fmt.Errorf("guard.writable: %q is not relative to home or under $VAR", e))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(m.Guard.Tools)) {
		t := m.Guard.Tools[name]
		if !slices.Contains(guard.Kinds, t.Kind) {
			errs = append(errs, fmt.Errorf("guard.tools.%s: kind %q is not shell|write|patch|read|glob", name, t.Kind))
		}
		if len(t.Fields) == 0 || slices.Contains(t.Fields, "") {
			errs = append(errs, fmt.Errorf("guard.tools.%s: fields must name the input fields the guard reads", name))
		}
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
		switch h.Timeout {
		case "", HookTimeoutResponse, HookTimeoutContext:
			if h.Timeout != "" && h.Respond == "" {
				errs = append(errs, fmt.Errorf("hooks[%d]: timeout needs respond", i))
			}
		default:
			errs = append(errs, fmt.Errorf("hooks[%d]: timeout %q is not response|context", i, h.Timeout))
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
		if t.PathField == "" || (len(t.Rules) == 0 && t.Usage == nil) {
			errs = append(errs, errors.New("jsonl_tail: needs path_field, and rules or usage"))
		}
		if t.Usage != nil {
			if err := t.Usage.validate(); err != nil {
				errs = append(errs, fmt.Errorf("jsonl_tail.usage: %w", err))
			}
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
		if r.Keys != "" && r.State != StateBlocked {
			errs = append(errs, fmt.Errorf("rules[%d] %s: keys answer a dialog: the rule must be blocked", i, r.ID))
		}
		if r.Regex != "" {
			if _, err := regexp.Compile(r.Regex); err != nil {
				errs = append(errs, fmt.Errorf("rules[%d] %s: %w", i, r.ID, err))
			}
		}
	}
	if a := m.Answer; a.Rule != "" || a.TextOption != "" || a.Submit != "" || a.TextKey != "" || a.TextFocus != "" {
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
		if (a.TextKey != "" || a.TextFocus != "") && a.TextOption == "" {
			errs = append(errs, errors.New("answer: text_key and text_focus need text_option"))
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

// launchData is what launch templates see: the LaunchSpec's fields and
// the manifest's .HookEvents.
type launchData struct {
	LaunchSpec
	HookEvents []string
}

// HookEvents are the hook events the manifest names, in manifest order,
// each once: those of [[hooks]], [[todos]] and todos_snapshot.on. A
// harness that registers hooks per event registers these, and an event
// none of them names isn't worth a `tm hook` process.
func (m *Manifest) HookEvents() []string {
	var out []string
	seen := map[string]bool{}
	add := func(ev string) {
		if ev != "" && !seen[ev] {
			seen[ev] = true
			out = append(out, ev)
		}
	}
	for _, h := range m.Hooks {
		add(h.Event)
	}
	for _, t := range m.Todos {
		add(t.Event)
	}
	if m.TodoSnapshot != nil {
		for _, ev := range m.TodoSnapshot.On {
			add(ev)
		}
	}
	return out
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
	if spec.Model == "" && !spec.AgentDefault {
		spec.Model = a.m.DefaultModel() // none chosen: the manifest's default
	}
	data := launchData{spec, a.m.HookEvents()}
	argv := []string{l.Command}
	add := func(tmpls []string) error {
		for _, t := range tmpls {
			s, err := render(t, data)
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
		s, err := render(v, data)
		if err != nil {
			return out, err
		}
		out.Env = append(out.Env, k+"="+s)
	}
	out.Files = map[string][]byte{}
	for _, f := range l.Files {
		s, err := render(f.Template, data)
		if err != nil {
			return out, fmt.Errorf("launch.files %s: %w", f.Path, err)
		}
		out.Files[f.Path] = []byte(s)
	}
	return out, nil
}

func (a *manifestAgent) Hook(ev HookEvent, env HookEnv) ([]Signal, HookResult, error) {
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
			out, err := renderRespond(h.Respond, ev, env, a.m.Hook)
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
	// hookexec is the exec form of a command hook, no shell on any OS:
	// the "command" and "args" members of the hook object, e.g.
	// hookexec .TMBin "hook" "--agent" "claude" ->
	// "command":"/bin/tm","args":["hook","--agent","claude"].
	"hookexec": func(command string, args ...string) (string, error) {
		if args == nil {
			args = []string{}
		}
		c, err := json.Marshal(command)
		if err != nil {
			return "", err
		}
		a, err := json.Marshal(args)
		return fmt.Sprintf(`"command":%s,"args":%s`, c, a), err
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
	// file is a file's text, e.g. the brief for an agent that takes it
	// as a value rather than a path: toml (file .BriefPath).
	"file": readText,
	// toml quotes a string as a TOML basic string, for a CLI that parses
	// a -c key=value as TOML (Codex's developer_instructions).
	"toml": TOMLString,
	// pathmodes is the file grants of an access policy as a TOML inline
	// table of path = "read" | "write", for a sandbox that takes one
	// mode per path (Codex's permission profile filesystem).
	"pathmodes": pathModes,
	// concat joins lists, for building one JSON array from several grants.
	"concat": func(lists ...[]string) []string {
		out := []string{}
		for _, l := range lists {
			out = append(out, l...)
		}
		return out
	},
}

// pathModes renders a's paths as {"/p"="read","/q"="write",…}: Read
// is read, Write is write, NoWrite and NoWriteFiles are read and win
// over a Write of the same path. Each path appears once, in policy
// order.
func pathModes(a Access) string {
	var order []string
	mode := map[string]string{}
	set := func(paths []string, m string, wins bool) {
		for _, p := range paths {
			old, seen := mode[p]
			if !seen {
				order = append(order, p)
			}
			if !seen || wins || old == "read" && m == "write" {
				mode[p] = m
			}
		}
	}
	set(a.Read, "read", false)
	set(a.Write, "write", false)
	set(a.NoWrite, "read", true)
	set(a.NoWriteFiles, "read", true)
	parts := make([]string, len(order))
	for i, p := range order {
		parts[i] = TOMLString(p) + "=" + TOMLString(mode[p])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// maxFileText bounds what the file template func reads: it ends up in
// an argv or a generated file.
const maxFileText = 256 << 10

func readText(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("file %q: not an absolute path", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileText+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxFileText {
		return "", fmt.Errorf("file %s: larger than %d bytes", path, maxFileText)
	}
	return string(b), nil
}

// TOMLString is s as a TOML basic string: quotes, backslashes and
// control characters escaped, invalid UTF-8 replaced.
func TOMLString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
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

// renderRespond renders a hook response. .Context and .Guard are
// methods so the (possibly expensive) context is rendered, and the call
// judged, only if the template uses them.
func renderRespond(tmpl string, ev HookEvent, env HookEnv, hook HookTrim) ([]byte, error) {
	s, err := render(tmpl, &respondData{ev: ev, env: env, hook: hook})
	return []byte(s), err
}

type respondData struct {
	ev     HookEvent
	env    HookEnv
	hook   HookTrim
	judged bool
	denial *guard.Denial
}

func (d *respondData) Event() string           { return d.ev.Event }
func (d *respondData) Payload() map[string]any { return d.ev.Payload }
func (d *respondData) Context() (string, error) {
	if d.env.Context == nil {
		return "", nil
	}
	b, err := d.env.Context()
	return string(b), err
}

// Guard is the guard's refusal of the tool call in the payload (the
// [hook] tool_field and input_field), or nil: a template answers with it, e.g.
// {{with .Guard}}…{{json .Message}}…{{end}}. The call is judged (and a
// refusal recorded) once however often the template asks.
func (d *respondData) Guard() *guard.Denial {
	if d.judged || d.env.Guard == nil {
		return d.denial
	}
	d.judged = true
	tool, _ := d.ev.Payload[d.hook.ToolField()].(string)
	input, _ := d.ev.Payload[d.hook.InputField()].(map[string]any)
	if tool != "" {
		d.denial = d.env.Guard(tool, input)
	}
	return d.denial
}
