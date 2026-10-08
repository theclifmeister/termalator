package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tracker merges the signals of one session's sources into its state
// (docs/SPEC.md §8.4). It is agent-neutral: the session feeds it what the
// agent's hooks, status file, JSONL tail and screen rules produced, and
// the process exit. It is safe for concurrent use.
//
// The precedence is fixed: exit > status file > hooks > JSONL tail >
// screen. A working status file that no turn explains (the hooks
// say idle and nothing has started since) yields to the hooks after
// statusUnconfirmed. A visible blocker on screen overrides any non-blocked state,
// background counters turn idle into working, and so does a kickoff prompt
// the agent hasn't started on yet (AwaitKickoff).
//
// A session with the agent's mod (ExpectMod) has one more source above
// them all but exit: the mod's own state reports (Mod). While its
// heartbeat holds, the mod decides; once it lapses, or before the mod is
// heard from, the other sources do (docs/SPEC.md §8.4 rule 0).
type Tracker struct {
	mu  sync.Mutex
	now func() time.Time

	exit     *Signal
	status   *Signal   // last good status-file reading; nil when invalid
	statusWk time.Time // when the status file's current run of working began
	statusEr string    // why the status file is not used, for explain
	hook     *Signal   // last level (non-transient) hook signal
	edge     *Signal   // last transient hook signal
	tail     *Signal
	screen   screenObs
	seq      map[string]uint64
	counters map[string]map[string]bool // name -> keys running
	agentSID string
	sidAt    time.Time
	todos    []Todo
	events   []EventRecord
	kickoff  kickoffWait
	mod      modObs
}

// modObs is the mod source: its last state and when it was last heard
// from (a transition or a heartbeat).
type modObs struct {
	on    bool    // the session runs the mod (ExpectMod)
	last  *Signal // the last state reported; At is when it took that state
	event string  // the mod event behind it, e.g. "turn.complete"
	seen  time.Time
}

// ModTimeout is how long the mod's word stands without a heartbeat or a
// report. The mod beats every ModBeat; a few missed beats mean it
// crashed, was unloaded or its channel broke, and the tracker falls back
// to the other sources.
const (
	ModBeat    = 10 * time.Second
	ModTimeout = 3*ModBeat + 5*time.Second
)

// kickoffWait is a first prompt, given at launch, that the agent hasn't
// started on yet. Claude reports idle at startup (SessionStart, its status
// file) before it takes the prompt from its command line; without this the
// session looks idle, e.g. not counted against the parallel threads cap,
// until the prompt's turn starts.
type kickoffWait struct {
	on        bool
	idleSince time.Time // when the agent first looked idle while waiting
}

// ReasonKickoff is the reason of the working state an idle agent shows
// while its kickoff prompt is pending (Tracker.AwaitKickoff).
const ReasonKickoff = "kickoff"

// kickoffGrace is how long an agent may look idle with its kickoff still
// pending before the tracker believes it: an agent that never starts on
// the prompt must not look busy forever.
const kickoffGrace = 2 * time.Minute

// EventRecord is one hook event as the tracker saw it, for explain.
type EventRecord struct {
	At      time.Time `json:"at"`
	Seq     uint64    `json:"seq"`
	Event   string    `json:"event"`
	Detail  string    `json:"detail,omitempty"` // e.g. tool_name=Write
	Signals []string  `json:"signals,omitempty"`
}

// maxEvents is how many hook events explain keeps.
const maxEvents = 30

// statusLag is how long a hook newer than the status file may stand in
// for it. Claude updates its file within ~100 ms of the hook.
const statusLag = time.Second

// statusUnconfirmed is how long the status file's working may stand
// against an idle hook with no sign of a new turn (no UserPromptSubmit,
// no tool call since): Claude marks its file busy for a slash command
// such as /remote-control, which runs no turn and fires no Stop, and
// may leave it so (docs/SPEC.md §8.4 rule 2).
const statusUnconfirmed = 5 * time.Second

// Screen debounce (docs/SPEC.md §8.4 rule 6): leaving working for idle on
// screen evidence alone needs this many evaluations in a row, or this long.
const (
	screenDebounceEvals = 3
	screenDebounceTime  = 700 * time.Millisecond
)

type screenObs struct {
	stable   *ScreenSignal // the debounced screen state
	cand     *ScreenSignal // a candidate waiting out the debounce
	candN    int
	candAt   time.Time
	last     *ScreenSignal // the latest evaluation, debounced or not
	matches  []string      // every rule that matched last time
	evalAt   time.Time
	stableAt time.Time
}

// ScreenSignal is one evaluation of the screen rules: the winning rule,
// or none (Rule == "").
type ScreenSignal struct {
	Rule   string `json:"rule,omitempty"`
	State  State  `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// NewTracker returns an empty tracker. now is the clock; nil means
// time.Now.
func NewTracker(now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	return &Tracker{now: now, seq: map[string]uint64{}, counters: map[string]map[string]bool{}}
}

// AwaitKickoff says the agent was launched with a first prompt: until a
// source sees it working or blocked, an idle state reads as working,
// reason ReasonKickoff (for kickoffGrace at most).
func (t *Tracker) AwaitKickoff() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.kickoff = kickoffWait{on: true}
}

// sawState ends the kickoff wait once a source sees the agent at work.
// The screen's blocked state doesn't count: a trust dialog comes before
// the kickoff.
func (t *Tracker) sawState(s State, screen bool) {
	if s == StateWorking || (s == StateBlocked && !screen) {
		t.kickoff.on = false
	}
}

// ExpectMod says the session runs the agent's mod. Its command hooks
// then carry no turn events, so their level states (SessionStart's idle)
// would only mislead the fallback: Hook ignores them, except exited.
func (t *Tracker) ExpectMod() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.mod.on = true
}

// Mod records a state report from the mod (event names what it saw:
// "turn.start", "beat" for a heartbeat). A report of the state the mod
// already holds only renews its heartbeat.
func (t *Tracker) Mod(s State, reason, event string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.mod.seen = now
	if l := t.mod.last; l != nil && l.State == s && l.Reason == reason {
		return
	}
	t.mod.last = &Signal{Source: "mod", State: s, Reason: reason, At: now}
	t.mod.event = event
	t.sawState(s, false)
	t.events = append(t.events, EventRecord{At: now, Event: "mod " + event, Signals: []string{stateText(s, reason)}})
	if len(t.events) > maxEvents {
		t.events = t.events[len(t.events)-maxEvents:]
	}
}

// ModLive reports whether the session runs the mod and it was heard
// from within ModTimeout: then it, not the paste injector, delivers
// queued prompts (docs/SPEC.md §8.6).
func (t *Tracker) ModLive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.mod.on && t.modLive(t.now())
}

// modLive reports whether the mod's word stands: heard from within
// ModTimeout.
func (t *Tracker) modLive(now time.Time) bool {
	return t.mod.last != nil && now.Sub(t.mod.seen) < ModTimeout
}

// Exited records the process exit.
func (t *Tracker) Exited(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.exit = &Signal{Source: "exit", State: StateExited, Reason: reason, At: t.now()}
}

// Hook applies the signals of one hook event. It returns true when the
// agent's session id changed.
func (t *Tracker) Hook(ev HookEvent, sigs []Signal) (sidChanged bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	at := ev.At
	if at.IsZero() {
		at = t.now()
	}
	if ev.Seq != 0 && ev.Seq <= t.seq["hook"] {
		return false
	}
	if ev.Seq != 0 {
		t.seq["hook"] = ev.Seq
	}
	rec := EventRecord{At: at, Seq: ev.Seq, Event: ev.Event, Detail: eventDetail(ev.Payload)}
	for _, s := range sigs {
		s.At = at
		switch {
		case s.AgentSID != "":
			if s.AgentSID != t.agentSID {
				t.agentSID, sidChanged = s.AgentSID, true
				rec.Signals = append(rec.Signals, "session "+s.AgentSID)
			}
			t.sidAt = at
		case s.Counter != "":
			t.count(s.Counter, s.CounterKey)
			rec.Signals = append(rec.Signals, s.Counter+" "+s.CounterKey)
		case s.Todo != nil:
			t.todos = ApplyTodo(t.todos, *s.Todo)
			rec.Signals = append(rec.Signals, "todo "+string(s.Todo.Op))
		case s.State != "" && t.mod.on && s.State != StateExited:
			rec.Signals = append(rec.Signals, stateText(s.State, s.Reason)+" (ignored: the mod reports state)")
		case s.State != "":
			sc := s
			t.sawState(s.State, false)
			if s.Transient {
				t.edge = &sc
				rec.Signals = append(rec.Signals, string(s.State)+" (transient)")
			} else {
				t.hook = &sc
				rec.Signals = append(rec.Signals, stateText(s.State, s.Reason))
			}
		}
	}
	t.events = append(t.events, rec)
	if len(t.events) > maxEvents {
		t.events = t.events[len(t.events)-maxEvents:]
	}
	return sidChanged
}

// Ignore records a hook event that changes nothing, with why, for
// explain.
func (t *Tracker) Ignore(ev HookEvent, why string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ev.Seq != 0 && ev.Seq <= t.seq["hook"] {
		return
	}
	if ev.Seq != 0 {
		t.seq["hook"] = ev.Seq
	}
	at := ev.At
	if at.IsZero() {
		at = t.now()
	}
	t.events = append(t.events, EventRecord{At: at, Seq: ev.Seq, Event: ev.Event, Detail: eventDetail(ev.Payload), Signals: []string{"ignored: " + why}})
	if len(t.events) > maxEvents {
		t.events = t.events[len(t.events)-maxEvents:]
	}
}

func eventDetail(p map[string]any) string {
	var parts []string
	for _, k := range []string{"source", "reason", "notification_type", "tool_name", "tool_use_id", "agent_type"} {
		if s, ok := lookupString(p, k); ok && s != "" {
			parts = append(parts, k+"="+s)
		}
	}
	return strings.Join(parts, " ")
}

func (t *Tracker) count(c, key string) {
	name := c[1:]
	m := t.counters[name]
	if m == nil {
		m = map[string]bool{}
		t.counters[name] = m
	}
	if key == "" {
		key = "-"
	}
	if c[0] == '+' {
		m[key] = true
	} else {
		delete(m, key) // a repeated end can't go below zero
	}
}

// Status records a status-file reading; written is when the agent wrote
// the file (its mtime; zero means now). err non-nil (missing,
// unparsable, untested version) makes the tracker ignore the file until
// the next good reading. It returns true when the agent's session id
// changed.
func (t *Tracker) Status(r *StatusReading, written time.Time, err error) (sidChanged bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		t.status, t.statusEr = nil, err.Error()
		return false
	}
	s := r.Signal
	s.At = written
	if s.At.IsZero() {
		s.At = t.now()
	}
	if s.State == StateWorking && (t.status == nil || t.status.State != StateWorking) {
		t.statusWk = s.At
	}
	t.status, t.statusEr = &s, ""
	t.sawState(s.State, false)
	if s.AgentSID != "" && s.AgentSID != t.agentSID && !t.sidAt.After(s.At) {
		t.agentSID, sidChanged = s.AgentSID, true
		t.sidAt = s.At
	}
	return sidChanged
}

// Tail records a signal from the JSONL tail.
func (t *Tracker) Tail(s Signal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s.At = t.now()
	t.tail = &s
	t.sawState(s.State, false)
}

// Screen records one evaluation of the screen rules. matches lists every
// rule that matched, for explain.
func (t *Tracker) Screen(s ScreenSignal, matches []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	o := &t.screen
	ss := s
	o.last, o.matches, o.evalAt = &ss, matches, now
	t.sawState(s.State, true)
	if s.Rule == "" || s.State == StateUnknown {
		// The screen can't tell: it abstains.
		o.stable, o.cand, o.candN = nil, nil, 0
		return
	}
	if o.stable != nil && o.stable.State == s.State && o.stable.Reason == s.Reason {
		o.stable, o.cand, o.candN = &ss, nil, 0
		return
	}
	// Only working -> idle is debounced: a spinner frame that is briefly
	// missing must not end a turn.
	if o.stable == nil || !(o.stable.State == StateWorking && s.State == StateIdle) {
		o.stable, o.stableAt, o.cand, o.candN = &ss, now, nil, 0
		return
	}
	if o.cand == nil || o.cand.State != s.State {
		o.cand, o.candN, o.candAt = &ss, 0, now
	}
	o.candN++
	if o.candN >= screenDebounceEvals || now.Sub(o.candAt) >= screenDebounceTime {
		o.stable, o.stableAt, o.cand, o.candN = &ss, now, nil, 0
	}
}

// SetTodos replaces the mirrored list (the snapshot re-read).
func (t *Tracker) SetTodos(list []Todo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.todos = append([]Todo{}, list...)
}

// SetAgentSID sets the agent's session id, e.g. the pre-assigned one.
func (t *Tracker) SetAgentSID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.agentSID = id
}

// AgentSID is the agent's latest session id.
func (t *Tracker) AgentSID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.agentSID
}

// Todos returns a copy of the mirrored todo list.
func (t *Tracker) Todos() []Todo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Todo{}, t.todos...)
}

// Merged is the arbitration result.
type Merged struct {
	State  State  `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Sources names the sources the state was derived from, e.g.
	// "status_file+screen" or "hooks+screen".
	Sources string `json:"sources,omitempty"`
}

// State arbitrates the current state.
func (t *Tracker) State() Merged {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.merge()
}

func (t *Tracker) merge() Merged {
	if t.exit != nil {
		return Merged{State: StateExited, Reason: t.exit.Reason, Sources: "exit"}
	}
	if t.hook != nil && t.hook.State == StateExited {
		return Merged{State: StateExited, Reason: t.hook.Reason, Sources: "hooks"}
	}
	if t.modLive(t.now()) {
		return t.mergeMod()
	}
	var m Merged
	var used []string
	scr := t.screen.stable
	switch {
	case t.status != nil && t.hook != nil && t.hook.At.After(t.status.At) && t.now().Sub(t.hook.At) < statusLag:
		// The agent writes its status file a moment after the hook
		// fires: a hook newer than the file wins until the file catches
		// up, or for statusLag at most.
		m.State, m.Reason = t.hook.State, t.hook.Reason
		used = append(used, "hooks")
	case t.statusDoubt(t.now()) != "":
		// Rule 2a: a working status file no turn explains.
		m.State, m.Reason = t.hook.State, t.hook.Reason
		used = append(used, "hooks")
	case t.status != nil:
		// Rule 2: the status file is the primary level signal.
		m.State, m.Reason = t.status.State, t.status.Reason
		used = append(used, "status_file")
	default:
		var level *Signal
		src := ""
		if t.hook != nil {
			level, src = t.hook, "hooks"
		}
		// The transcript cross-checks hooks: an interrupt or turn end
		// written after the last hook level signal wins (no hook fires on
		// an Esc).
		if t.tail != nil && (level == nil || t.tail.At.After(level.At)) {
			level, src = t.tail, "jsonl_tail"
		}
		if level != nil {
			m.State, m.Reason = level.State, level.Reason
			used = append(used, src)
			// A tool call after the last level signal means a turn is
			// running (e.g. a permission was granted).
			if t.edge != nil && t.edge.At.After(level.At) && level.State != StateExited {
				m.State, m.Reason = StateWorking, ""
				if src != "hooks" {
					used = append(used, "hooks")
				}
			}
			// A blocked state only hooks saw expires once the screen,
			// settled after it, shows something else: Claude fires no
			// hook when a dialog is dismissed with Esc.
			if m.State == StateBlocked && scr != nil && scr.State != StateBlocked && t.screen.stableAt.After(level.At) {
				m.State, m.Reason = scr.State, scr.Reason
				used = append(used, "screen")
			}
		} else if t.edge != nil {
			m.State = StateWorking
			used = append(used, "hooks")
		} else if scr != nil {
			// Rule 5: the screen as the last resort.
			m.State, m.Reason = scr.State, scr.Reason
			used = append(used, "screen")
		}
	}
	// Rule 3: a visible blocker overrides any non-blocked state.
	if scr != nil && scr.State == StateBlocked && m.State != StateBlocked {
		m.State, m.Reason = StateBlocked, scr.Reason
		used = append(used, "screen")
	}
	return t.settle(m, used)
}

// mergeMod is the state while the mod's word stands (rule 0): the mod's,
// with two cross-checks for what it can't see, then rules 4 and 4b.
func (t *Tracker) mergeMod() Merged {
	l := t.mod.last
	m := Merged{State: l.State, Reason: l.Reason}
	used := []string{"mod"}
	scr := t.screen.stable
	switch {
	case l.State == StateExited:
		return Merged{State: StateExited, Reason: l.Reason, Sources: "mod"}
	case l.State == StateBlocked:
		// A permission granted fires nothing until the tool ends: the
		// status file or the screen, newer than the mod's blocked,
		// showing working ends it.
		if s := t.status; s != nil && s.State == StateWorking && s.At.After(l.At) {
			m.State, m.Reason = StateWorking, ""
			used = append(used, "status_file")
		} else if scr != nil && scr.State == StateWorking && t.screen.stableAt.After(l.At) {
			m.State, m.Reason = StateWorking, ""
			used = append(used, "screen")
		}
	case l.State == StateIdle && scr != nil && scr.State == StateBlocked:
		// Rule 3, for idle only: a dialog no mod event covers (trust,
		// a slash command's menu).
		m.State, m.Reason = StateBlocked, scr.Reason
		used = append(used, "screen")
	}
	return t.settle(m, used)
}

// settle applies rules 4 and 4b to a merged state and names its sources.
func (t *Tracker) settle(m Merged, used []string) Merged {
	// Rule 4: background activity.
	if m.State == StateIdle && t.running() > 0 {
		m.State, m.Reason = StateWorking, "background"
		used = append(used, "counters")
	}
	// Rule 4b: a kickoff prompt not started on yet.
	if m.State == StateIdle && t.kickoff.on {
		now := t.now()
		if t.kickoff.idleSince.IsZero() {
			t.kickoff.idleSince = now
		}
		if now.Sub(t.kickoff.idleSince) < kickoffGrace {
			m.State, m.Reason = StateWorking, ReasonKickoff
			used = append(used, "kickoff")
		}
	}
	if m.State == "" {
		m.State = StateUnknown
	}
	m.Sources = strings.Join(dedupe(used), "+")
	return m
}

// statusDoubt says why the status file's working is not believed, or
// "": the last hook level signal says idle, no turn has started since
// (a UserPromptSubmit would have replaced it, a tool call shows as a
// newer edge), and the file has said working for statusUnconfirmed
// after that idle hook. Counters still turn the result into working.
func (t *Tracker) statusDoubt(now time.Time) string {
	if t.status == nil || t.status.State != StateWorking || t.hook == nil || t.hook.State != StateIdle {
		return ""
	}
	if t.edge != nil && t.edge.At.After(t.hook.At) {
		return ""
	}
	since := t.statusWk
	if t.hook.At.After(since) {
		since = t.hook.At
	}
	if now.Sub(since) < statusUnconfirmed {
		return ""
	}
	return fmt.Sprintf("status file says working since %s, but no turn started after the idle hook at %s",
		t.statusWk.Format(time.TimeOnly), t.hook.At.Format(time.TimeOnly))
}

func (t *Tracker) running() int {
	n := 0
	for _, m := range t.counters {
		n += len(m)
	}
	return n
}

func dedupe(s []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func stateText(s State, reason string) string {
	if reason != "" {
		return string(s) + "/" + reason
	}
	return string(s)
}

// Explanation is what `tm agent explain` prints: the last signal of every
// source, the screen rules that matched, and the arbitration result.
type Explanation struct {
	Result     Merged      `json:"result"`
	Exit       *SourceView `json:"exit,omitempty"`
	StatusFile *SourceView `json:"status_file,omitempty"`
	StatusErr  string      `json:"status_file_error,omitempty"`
	// StatusDoubt says why a working status file is not believed
	// (§8.4 rule 2).
	StatusDoubt string            `json:"status_file_doubt,omitempty"`
	Hook        *SourceView       `json:"hook,omitempty"`
	HookEdge    *SourceView       `json:"hook_transient,omitempty"`
	Tail        *SourceView       `json:"jsonl_tail,omitempty"`
	Screen      *SourceView       `json:"screen,omitempty"`
	ScreenLast  *SourceView       `json:"screen_last,omitempty"` // before the debounce
	Matches     []string          `json:"screen_matches,omitempty"`
	Counters    map[string]int    `json:"counters,omitempty"`
	AgentSID    string            `json:"agent_session_id,omitempty"`
	Todos       []Todo            `json:"todos,omitempty"`
	Events      []EventRecord     `json:"events,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`

	// Mod is the mod source, in a session that runs the mod.
	Mod *ModView `json:"mod,omitempty"`
}

// ModView is the mod source for explain: its last state, the event
// behind it, when it was last heard from, and whether its word stands.
type ModView struct {
	SourceView
	Event string    `json:"event,omitempty"`
	Seen  time.Time `json:"seen,omitempty"`
	Live  bool      `json:"live"`
}

// SourceView is one source's last signal.
type SourceView struct {
	State  State     `json:"state"`
	Reason string    `json:"reason,omitempty"`
	Rule   string    `json:"rule,omitempty"`
	At     time.Time `json:"at"`
}

func view(s *Signal) *SourceView {
	if s == nil {
		return nil
	}
	return &SourceView{State: s.State, Reason: s.Reason, At: s.At}
}

// Explain returns the tracker's view of every source.
func (t *Tracker) Explain() Explanation {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := Explanation{
		Result:      t.merge(),
		Exit:        view(t.exit),
		StatusFile:  view(t.status),
		StatusErr:   t.statusEr,
		StatusDoubt: t.statusDoubt(t.now()),
		Hook:        view(t.hook),
		HookEdge:    view(t.edge),
		Tail:        view(t.tail),
		Matches:     append([]string{}, t.screen.matches...),
		AgentSID:    t.agentSID,
		Todos:       append([]Todo{}, t.todos...),
		Events:      append([]EventRecord{}, t.events...),
	}
	if s := t.screen.stable; s != nil {
		e.Screen = &SourceView{State: s.State, Reason: s.Reason, Rule: s.Rule, At: t.screen.stableAt}
	}
	if s := t.screen.last; s != nil {
		e.ScreenLast = &SourceView{State: s.State, Reason: s.Reason, Rule: s.Rule, At: t.screen.evalAt}
	}
	if t.mod.on || t.mod.last != nil {
		e.Mod = &ModView{Seen: t.mod.seen, Event: t.mod.event, Live: t.modLive(t.now())}
		if l := t.mod.last; l != nil {
			e.Mod.SourceView = *view(l)
		}
	}
	for name, m := range t.counters {
		if len(m) > 0 {
			if e.Counters == nil {
				e.Counters = map[string]int{}
			}
			e.Counters[name] = len(m)
		}
	}
	sort.Strings(e.Matches)
	return e
}
