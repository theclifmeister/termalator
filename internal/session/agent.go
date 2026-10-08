package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/detect"
	"github.com/theclifmeister/terminatr/internal/guard"
	"github.com/theclifmeister/terminatr/internal/pty"
)

// AgentConfig attaches an agent to a session: the session then runs the
// agent's declarative sources (status file, JSONL tail, todo snapshot,
// screen rules), takes its hook events and arbitrates them into one state
// (docs/SPEC.md §8.4). Nothing here knows a specific agent.
type AgentConfig struct {
	Agent agent.Agent
	// AgentSID is the agent's own session id, when pre-assigned or resumed.
	AgentSID string
	// Kickoff says the agent was launched with a first prompt: it reads
	// as working until it starts on it (agent.Tracker.AwaitKickoff).
	Kickoff bool
	// Home is the user's home directory, for status-file and snapshot
	// path templates.
	Home string
	// Context renders the role's context for hook responses (§7.8).
	Context func() ([]byte, error)
	// Guard judges a tool call a hook reports against the session's
	// guard rules, for hook responses (agent.HookEnv.Guard).
	Guard func(tool string, input map[string]any) *guard.Denial
	// OnChange is called (without locks held) when the merged state or
	// the agent's session id changes.
	OnChange func(*Session)
	// PromptHold bounds how long a queued prompt may be held while the
	// agent is idle (a prompt box with text in it, a dialog on screen)
	// before it is resolved: sent through the agent's channel when it
	// allows that, else dropped (docs/SPEC.md §8.6). Zero means
	// DefaultPromptHold.
	PromptHold time.Duration
	// OnPromptResolved is called (without locks held) when a held prompt
	// was resolved after PromptHold.
	OnPromptResolved func(*Session, PromptResolution)
	// ModSocket, when set, says the agent runs terminatr's mod, which
	// reports its state over this socket (ModState; docs/SPEC.md §8.6).
	ModSocket string
}

// DefaultPromptHold is AgentConfig.PromptHold's default.
const DefaultPromptHold = 10 * time.Minute

// Why a queued prompt is held while the agent is idle.
const (
	HeldBox    = "prompt box not empty"
	HeldDialog = "dialog on screen"
	// HeldMod: the mod took the prompt and handed it to the agent, which
	// is idle and hasn't run it (modprompt.go).
	HeldMod = "the mod took it, the agent hasn't run it"
)

// PromptOptions qualify a prompt.
type PromptOptions struct {
	// Channel lets a prompt that stays held go through the agent's
	// structured channel (agent.Agent.Prompt), when it has one, instead
	// of being dropped: for the server's own fixed-word prompts, which
	// may arrive framed as from another session. A slash command or the
	// human's own words never take it.
	Channel bool
	// Refresh, when set, is called right before the prompt is delivered
	// (pasted or sent through the channel), however long it waited: it
	// returns the text to deliver, or false when the prompt is stale and
	// is dropped (e.g. a nudge whose items were all handled meanwhile).
	Refresh func() (string, bool)
}

// PromptResolution is what became of a prompt held for PromptHold.
type PromptResolution struct {
	Text string
	// Via is "sent" (written to the agent's channel, which may not
	// confirm delivery: Claude's socket never answers), "dropped" or
	// "stale" (Refresh said so).
	Via    string
	Why    string // HeldBox or HeldDialog
	Held   time.Duration
	Queued time.Time
	Err    error // why the channel failed, for a drop that tried it
}

// queuedPrompt is one prompt waiting for the mod or the paste injector.
type queuedPrompt struct {
	id      string
	text    string
	at      time.Time
	channel bool
	refresh func() (string, bool)
	// The mod's side (modprompt.go): when it was last offered to the mod,
	// whether the mod took it (stored it and handed it to Claude),
	// whether its text was refreshed already, and whether it goes to the
	// paste injector after all (the mod refused it or never answered).
	offered   time.Time
	taken     bool
	refreshed bool
	paste     bool
}

// Timings of the agent sources (docs/SPEC.md §8.3–8.4).
const (
	sourceTick      = 100 * time.Millisecond // status file stat, tail read, prompt queue
	statusReread    = 500 * time.Millisecond // read the status file even without a change
	screenInterval  = 300 * time.Millisecond // at most one screen evaluation per interval
	screenIdleEval  = time.Second            // and one per second without output
	pasteEnterDelay = 150 * time.Millisecond // Enter as a separate write after a paste
	identifyEvery   = time.Second            // identify-by-process for shells
)

// probeEvery spaces the agent's liveness checks (its pid, and the
// agent's own probe: Claude's messaging socket). A variable for tests.
var probeEvery = 5 * time.Second

// agentRT is the agent side of one session.
type agentRT struct {
	a        agent.Agent
	src      *agent.Sources
	man      *agent.Manifest
	eng      *detect.Engine
	tr       *agent.Tracker
	cfg      AgentConfig
	pid      int  // the agent's pid (the session's, or a foreground job's)
	observed bool // identified by process: no hooks, no launch
	seq      atomic.Uint64
	stop     chan struct{}
	stopOnce sync.Once

	mu         sync.Mutex
	tailPath   string
	tailOff    int64
	statusMod  time.Time
	statusSize int64
	statusRead time.Time
	fields     map[string]string // status-file fields while trusted
	token      string            // the prompt channel's token, from the hooks
	probedAt   time.Time
	pidGone    string         // why the agent's pid is gone; the status file is then stale
	live       agent.Liveness // the agent's own probe (agent.Prober)
	liveErr    string
	version    string
	prompts    []queuedPrompt
	promptSeq  uint64    // the last queued prompt's number
	promptGen  string    // tells this agent's prompt ids from an earlier one's
	heldSince  time.Time // the head prompt has been held while idle since
	heldWhy    string
	modPolls   int       // the mod's polls for prompts in flight (modprompt.go)
	modPolled  time.Time // when the mod's last poll ended
	stallAt    time.Time // the queued-at of the head prompt whose stall was logged
	lastState  agent.Merged
	lastSID    string
	lastTodos  []agent.Todo
	emptyBox   bool // the empty-box rule matched at the last evaluation
	blocker    bool // a blocked rule matched at the last evaluation
}

func newAgentRT(cfg AgentConfig, pid int, observed bool) (*agentRT, error) {
	eng, err := detect.New(cfg.Agent.Rules())
	if err != nil {
		return nil, err
	}
	rt := &agentRT{
		a: cfg.Agent, src: cfg.Agent.Sources(), man: agent.ManifestOf(cfg.Agent),
		eng: eng, tr: agent.NewTracker(nil), cfg: cfg, pid: pid, observed: observed,
		stop: make(chan struct{}), promptGen: strconv.FormatInt(time.Now().UnixNano(), 36),
	}
	if cfg.AgentSID != "" {
		rt.tr.SetAgentSID(cfg.AgentSID)
		rt.lastSID = cfg.AgentSID
	}
	if cfg.Kickoff && !observed {
		rt.tr.AwaitKickoff()
	}
	if cfg.ModSocket != "" && !observed {
		rt.tr.ExpectMod()
	}
	return rt, nil
}

func (rt *agentRT) close() { rt.stopOnce.Do(func() { close(rt.stop) }) }

// Agent returns the session's agent, or nil.
func (s *Session) Agent() agent.Agent {
	if rt := s.agentRT(); rt != nil {
		return rt.a
	}
	return nil
}

func (s *Session) agentRT() *agentRT {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ag
}

// AgentState is the merged state of the session's agent.
type AgentState struct {
	Agent    string
	Observed bool // identified by process rather than launched
	agent.Merged
	AgentSID string
	Todos    []agent.Todo
	Queued   int // prompts waiting for the paste injector
	// QueuedSince is when the oldest queued prompt was queued.
	QueuedSince time.Time
	// Held says why the next prompt isn't pasted although the agent is
	// idle (HeldBox, HeldDialog), since HeldSince; empty while it isn't.
	Held      string
	HeldSince time.Time
	// RemoteControl is the observed remote control state, when the
	// manifest names a status_field and the status file was read.
	RemoteControl, RemoteKnown bool
}

// AgentState returns the agent's merged state; ok is false for a session
// without an agent.
func (s *Session) AgentState() (st AgentState, ok bool) {
	rt := s.agentRT()
	if rt == nil {
		return st, false
	}
	rt.mu.Lock()
	queued := len(rt.prompts)
	var since time.Time
	if queued > 0 {
		since = rt.prompts[0].at
	}
	held, heldSince := rt.heldWhy, rt.heldSince
	var remote, known bool
	if m := agent.ManifestOf(rt.a); m != nil && m.ObservesRemote() && rt.fields != nil {
		remote, known = rt.fields[m.RemoteControl.StatusField] != "", true
	}
	rt.mu.Unlock()
	return AgentState{
		Agent: rt.a.Name(), Observed: rt.observed, Merged: rt.tr.State(),
		AgentSID: rt.tr.AgentSID(), Todos: rt.tr.Todos(), Queued: queued,
		QueuedSince: since, Held: held, HeldSince: heldSince,
		RemoteControl: remote, RemoteKnown: known,
	}, true
}

// Explain returns the agent tracker's view of every source.
func (s *Session) Explain() (agent.Explanation, bool) {
	rt := s.agentRT()
	if rt == nil {
		return agent.Explanation{}, false
	}
	e := rt.tr.Explain()
	rt.mu.Lock()
	e.Extra = map[string]string{"pid": fmt.Sprint(rt.pid)}
	if rt.tailPath != "" {
		e.Extra["jsonl_tail"] = rt.tailPath
	}
	if p, err := rt.statusPath(); err == nil && p != "" {
		e.Extra["status_file"] = p
	}
	if rt.version != "" {
		e.Extra["version"] = rt.version
	}
	if rt.cfg.ModSocket != "" {
		e.Extra["mod_socket"] = rt.cfg.ModSocket
	}
	if rt.observed {
		e.Extra["identified"] = "by process"
	}
	switch {
	case rt.pidGone != "":
		e.Extra["liveness"] = "gone: " + rt.pidGone
	case rt.live == agent.Gone:
		e.Extra["liveness"] = "gone: " + rt.liveErr
	case rt.live == agent.Live:
		e.Extra["liveness"] = "live (probe answered)"
	case rt.liveErr != "":
		e.Extra["liveness"] = "unknown: " + rt.liveErr
	}
	if len(rt.prompts) > 0 {
		q := fmt.Sprintf("%d, oldest since %s", len(rt.prompts), rt.prompts[0].at.Format(time.DateTime))
		if rt.heldWhy != "" {
			q += fmt.Sprintf("; held since %s: %s", rt.heldSince.Format(time.DateTime), rt.heldWhy)
		}
		e.Extra["queued_prompts"] = q
	}
	rt.mu.Unlock()
	return e, true
}

// ErrNoAgent is returned for agent operations on a plain session.
var ErrNoAgent = errors.New("session has no agent")

// Hook delivers one hook event to the session's agent and returns what
// the harness should get back. Events are numbered in arrival order; the
// hook endpoint is synchronous, so that is the agent's order.
func (s *Session) Hook(event string, payload map[string]any) (agent.HookResult, error) {
	rt := s.agentRT()
	if rt == nil || rt.observed {
		return agent.HookResult{}, ErrNoAgent
	}
	ev := agent.HookEvent{Agent: rt.a.Name(), Event: event, Payload: payload, Seq: rt.seq.Add(1), At: time.Now()}
	sigs, res, err := rt.a.Hook(ev, agent.HookEnv{Context: rt.cfg.Context, Guard: rt.cfg.Guard})
	rt.tr.Hook(ev, sigs)
	if t := rt.src.JSONLTail; t != nil {
		if p, ok := payload[t.PathField].(string); ok && p != "" && filepath.IsAbs(p) {
			rt.setTail(p)
		}
	}
	if snap := rt.src.TodoSnapshot; snap != nil && slices.Contains(snap.On, event) {
		rt.healTodos()
	}
	s.agentChanged(rt)
	return res, err
}

// ModState takes a state report from the agent's mod: a transition it
// saw (event names it, e.g. "turn.complete"), or its heartbeat ("beat",
// with the state it holds). A turn's end re-reads the todo snapshot, as
// the hooks' Stop did.
func (s *Session) ModState(state agent.State, reason, event string) error {
	rt := s.agentRT()
	if rt == nil || rt.observed {
		return ErrNoAgent
	}
	rt.tr.Mod(state, reason, event)
	if event == "turn.complete" && rt.src.TodoSnapshot != nil {
		rt.healTodos()
	}
	s.agentChanged(rt)
	return nil
}

// setTail follows a new JSONL file from its current end: what is already
// in it happened before this event.
func (rt *agentRT) setTail(p string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if p == rt.tailPath {
		return
	}
	rt.tailPath, rt.tailOff = p, 0
	if fi, err := os.Stat(p); err == nil {
		rt.tailOff = fi.Size()
	}
}

func (rt *agentRT) vars() agent.SourceVars {
	return agent.SourceVars{Home: rt.cfg.Home, PID: rt.pid, AgentSID: rt.tr.AgentSID()}
}

// healTodos re-reads the agent's own copy of its todo list. An empty or
// missing copy doesn't wipe the mirror: Claude deletes the files once a
// list is all done.
func (rt *agentRT) healTodos() {
	snap := rt.src.TodoSnapshot
	if rt.tr.AgentSID() == "" && strings.Contains(snap.Dir, "AgentSID") {
		return
	}
	dir, err := snap.DirFor(rt.vars())
	if err != nil {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	files := map[string][]byte{}
	for _, e := range ents {
		if e.Type().IsRegular() {
			if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
				files[e.Name()] = b
			}
		}
	}
	if list := snap.Parse(files); len(list) > 0 {
		rt.tr.SetTodos(snap.Heal(rt.tr.Todos(), list))
	}
}

func (rt *agentRT) statusPath() (string, error) {
	if rt.src.StatusFile == nil {
		return "", nil
	}
	return rt.src.StatusFile.PathFor(rt.vars())
}

// pollStatus reads the status file when it changed, or every
// statusReread. A missing or bad file makes the tracker skip it.
func (rt *agentRT) pollStatus(now time.Time) {
	f := rt.src.StatusFile
	if f == nil {
		return
	}
	p, err := rt.statusPath()
	if err != nil {
		rt.tr.Status(nil, time.Time{}, err)
		return
	}
	fi, err := os.Stat(p)
	if err != nil {
		rt.mu.Lock()
		rt.fields, rt.statusMod = nil, time.Time{}
		rt.mu.Unlock()
		rt.tr.Status(nil, time.Time{}, fmt.Errorf("status file: %w", err))
		return
	}
	rt.mu.Lock()
	changed := !fi.ModTime().Equal(rt.statusMod) || fi.Size() != rt.statusSize || now.Sub(rt.statusRead) >= statusReread
	rt.mu.Unlock()
	if !changed {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	r, err := f.Read(data, rt.src)
	rt.mu.Lock()
	if err == nil && rt.pidGone != "" {
		// A crashed agent leaves its last state behind.
		err = fmt.Errorf("status file: %s", rt.pidGone)
	}
	rt.statusMod, rt.statusSize, rt.statusRead = fi.ModTime(), fi.Size(), now
	if err != nil {
		rt.fields = nil
	} else {
		rt.fields = r.Fields
	}
	rt.version = f.Version(data)
	rt.mu.Unlock()
	if err != nil {
		rt.tr.Status(nil, time.Time{}, err)
		return
	}
	rt.tr.Status(&r, fi.ModTime(), nil)
}

// probeAgent checks, every probeEvery, that the agent behind the status
// file is still there: its pid, then the agent's own probe when it has
// one and the status file names what to probe. A gone pid makes the
// status file stale (pollStatus); a gone probe keeps prompts off the
// agent's channel. Both show in tm agent explain.
func (s *Session) probeAgent(rt *agentRT, now time.Time) {
	rt.mu.Lock()
	if !rt.probedAt.IsZero() && now.Sub(rt.probedAt) < probeEvery {
		rt.mu.Unlock()
		return
	}
	rt.probedAt = now
	rt.mu.Unlock()
	pidGone := ""
	if rt.pid > 0 && errors.Is(syscall.Kill(rt.pid, 0), syscall.ESRCH) {
		pidGone = fmt.Sprintf("pid %d is gone", rt.pid)
	}
	live, why := agent.LiveUnknown, ""
	if p, ok := rt.a.(agent.Prober); ok && pidGone == "" {
		if t := s.promptTarget(rt); t.Fields != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			var err error
			live, err = p.Probe(ctx, t)
			cancel()
			if err != nil {
				why = err.Error()
			}
		}
	}
	rt.mu.Lock()
	changed := pidGone != rt.pidGone || live != rt.live
	rt.pidGone, rt.live, rt.liveErr = pidGone, live, why
	rt.mu.Unlock()
	switch {
	case !changed:
	case pidGone != "":
		s.cfg.Logf("session %s: %s %s: its status file is stale", s.cfg.ID, rt.a.Name(), pidGone)
	case live == agent.Gone:
		s.cfg.Logf("session %s: %s probe: gone (%s)", s.cfg.ID, rt.a.Name(), why)
	}
}

// channelGone says why the agent's channel can't be used, when its
// probe found it gone.
func (rt *agentRT) channelGone() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	switch {
	case rt.pidGone != "":
		return errors.New(rt.pidGone)
	case rt.live == agent.Gone:
		return fmt.Errorf("agent gone: %s", rt.liveErr)
	}
	return nil
}

// readTail reads lines appended to the JSONL file since the last call.
func (rt *agentRT) readTail() {
	t := rt.src.JSONLTail
	if t == nil {
		return
	}
	rt.mu.Lock()
	p, off := rt.tailPath, rt.tailOff
	rt.mu.Unlock()
	if p == "" {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() < off {
		off = 0 // truncated or replaced
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return
	}
	// Only whole lines; a partial last line is read again next time.
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return
	}
	for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
		if sig, ok := t.Line(line); ok {
			rt.tr.Tail(sig)
		}
	}
	rt.mu.Lock()
	if rt.tailPath == p {
		rt.tailOff = off + int64(end) + 1
	}
	rt.mu.Unlock()
}

// evalScreen runs the screen rules on the emulator's current text.
func (s *Session) evalScreen(rt *agentRT) {
	s.mu.Lock()
	if s.term == nil {
		s.mu.Unlock()
		return
	}
	title := s.term.Title()
	rows, noDim, err := s.term.Rows()
	s.mu.Unlock()
	if err != nil {
		s.cfg.Logf("session %s: screen rules: %v", s.cfg.ID, err)
		return
	}
	best, all := rt.eng.Eval(detect.Screen{Title: title, Rows: rows, NoDim: noDim})
	var names []string
	empty, blocker := false, false
	for _, m := range all {
		names = append(names, m.Rule)
		if rt.man != nil && m.Rule == rt.man.Inject.EmptyRule {
			empty = true
		}
		if m.State == agent.StateBlocked {
			blocker = true
		}
	}
	rt.mu.Lock()
	rt.emptyBox, rt.blocker = empty, blocker
	rt.mu.Unlock()
	var sig agent.ScreenSignal
	if best != nil {
		sig = agent.ScreenSignal{Rule: best.Rule, State: best.State, Reason: best.Reason}
	}
	rt.tr.Screen(sig, names)
}

// agentChanged notifies the owner when the state, the agent's session
// id or the todo list moved.
func (s *Session) agentChanged(rt *agentRT) {
	st := rt.tr.State()
	sid := rt.tr.AgentSID()
	todos := rt.tr.Todos()
	rt.mu.Lock()
	changed := st != rt.lastState || sid != rt.lastSID || !slices.Equal(todos, rt.lastTodos)
	rt.lastState, rt.lastSID, rt.lastTodos = st, sid, todos
	rt.mu.Unlock()
	if changed {
		s.notifyState()
		if rt.cfg.OnChange != nil {
			rt.cfg.OnChange(s)
		}
	}
}

// runAgent drives the agent's sources until the session ends.
func (s *Session) runAgent(rt *agentRT) {
	tick := time.NewTicker(sourceTick)
	defer tick.Stop()
	var lastEval time.Time
	for {
		select {
		case <-rt.stop:
			return
		case <-s.done:
			return
		case now := <-tick.C:
			s.probeAgent(rt, now)
			rt.pollStatus(now)
			rt.readTail()
			dirty := s.output.Swap(false)
			if (dirty && now.Sub(lastEval) >= screenInterval) || now.Sub(lastEval) >= screenIdleEval {
				s.evalScreen(rt)
				lastEval = now
			}
			s.agentChanged(rt)
			s.deliverPrompts(rt, now)
		}
	}
}

// Prompt delivers a follow-up prompt (docs/SPEC.md §8.1, §8.6). A
// structured channel is tried first when the agent has one; otherwise,
// or when it fails, the prompt is queued. While the agent's mod is live
// the mod takes the queue's head and hands it to the agent, which runs it
// once idle (modprompt.go); otherwise the paste injector pastes it once
// the agent is idle, no dialog is visible and the prompt box is empty.
// It returns "channel" or "queued".
func (s *Session) Prompt(text string) (string, error) {
	return s.PromptWith(text, PromptOptions{})
}

// PromptWith is Prompt with options.
func (s *Session) PromptWith(text string, o PromptOptions) (string, error) {
	rt := s.agentRT()
	if rt == nil {
		return "", ErrNoAgent
	}
	if rt.tr.State().State == agent.StateExited {
		return "", ErrExited
	}
	switch rt.a.Injector() {
	case agent.InjectNone:
		return "", fmt.Errorf("agent %s takes no prompts", rt.a.Name())
	case agent.InjectChannel:
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := rt.a.Prompt(ctx, s.promptTarget(rt), text)
		cancel()
		if err == nil {
			return "channel", nil
		}
		s.cfg.Logf("session %s: prompt channel failed, pasting instead: %v", s.cfg.ID, err)
	}
	rt.mu.Lock()
	rt.promptSeq++
	id := fmt.Sprintf("%s-%d", rt.promptGen, rt.promptSeq)
	rt.prompts = append(rt.prompts, queuedPrompt{id: id, text: text, at: time.Now(), channel: o.Channel, refresh: o.Refresh})
	rt.mu.Unlock()
	return "queued", nil
}

// promptTarget is what the agent's channel needs to reach it now.
func (s *Session) promptTarget(rt *agentRT) agent.PromptTarget {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return agent.PromptTarget{SessionID: s.cfg.ID, AgentSID: rt.tr.AgentSID(), PID: rt.pid,
		Version: rt.version, Fields: rt.fields, Token: rt.token}
}

// SetPromptToken records the prompt channel's token that the agent's
// hooks reported (agent.PromptTarget.Token). It lives only as long as
// the agent does, and is never logged.
func (s *Session) SetPromptToken(token string) {
	rt := s.agentRT()
	if rt == nil || rt.observed {
		return
	}
	rt.mu.Lock()
	rt.token = token
	rt.mu.Unlock()
}

// deliverPrompts pastes the first queued prompt when it is safe: idle,
// no blocker on screen (an Enter would answer it) and, where the agent
// names an empty-box rule, an empty box. A prompt held that way while the
// agent is idle for PromptHold (text left in the box, say, while the user
// drives the agent from elsewhere) is resolved, so it can't hold the
// queue forever: through the channel when it allows that, else dropped.
func (s *Session) deliverPrompts(rt *agentRT, now time.Time) {
	st := rt.tr.State()
	idle := st.State == agent.StateIdle
	rt.mu.Lock()
	if len(rt.prompts) == 0 {
		rt.heldSince, rt.heldWhy = time.Time{}, ""
		rt.mu.Unlock()
		return
	}
	if s.modDeliversLocked(rt, now) {
		// The mod has the head; the box and dialogs are no concern of a
		// prompt Claude queues itself. Taken while the agent idles, it is
		// held, and pasted after PromptHold: Claude runs what it queued
		// once idle, so it lost this one.
		p := &rt.prompts[0]
		if !idle || !p.taken {
			rt.heldSince, rt.heldWhy = time.Time{}, ""
			rt.mu.Unlock()
			return
		}
		if rt.heldWhy != HeldMod {
			rt.heldSince, rt.heldWhy = now, HeldMod
		}
		held := now.Sub(rt.heldSince)
		if held < rt.promptHold() {
			rt.mu.Unlock()
			return
		}
		p.paste, p.taken = true, false
		id := p.id
		rt.heldSince, rt.heldWhy = time.Time{}, ""
		rt.mu.Unlock()
		s.cfg.Logf("session %s: prompt %s held %s (%s): pasting it", s.cfg.ID, id, held.Round(time.Second), HeldMod)
		return
	}
	why := ""
	switch {
	case rt.blocker:
		why = HeldDialog
	case rt.man != nil && rt.man.Inject.EmptyRule != "" && !rt.emptyBox:
		why = HeldBox
	}
	if !idle || why == "" {
		// Working, blocked or exited: the prompt waits its turn.
		rt.heldSince, rt.heldWhy = time.Time{}, ""
	}
	if !idle {
		head := rt.prompts[0].at
		stalled := st.State == agent.StateWorking && now.Sub(head) >= promptStall && !rt.stallAt.Equal(head)
		if stalled {
			rt.stallAt = head
		}
		rt.mu.Unlock()
		if stalled {
			s.logStall(rt, st, now.Sub(head))
		}
		return
	}
	if why != "" {
		if rt.heldSince.IsZero() {
			rt.heldSince = now
		}
		rt.heldWhy = why
		if now.Sub(rt.heldSince) < rt.promptHold() {
			rt.mu.Unlock()
			return
		}
	}
	p := rt.prompts[0]
	rt.prompts = rt.prompts[1:]
	held := now.Sub(rt.heldSince)
	rt.heldSince, rt.heldWhy = time.Time{}, ""
	if why == "" {
		rt.emptyBox = false // until the screen says so again
	}
	rt.mu.Unlock()
	if p.refresh != nil {
		text, ok := p.refresh()
		if !ok {
			s.cfg.Logf("session %s: queued prompt (queued %s) is stale: not delivered", s.cfg.ID, p.at.Format(time.DateTime))
			if rt.cfg.OnPromptResolved != nil {
				rt.cfg.OnPromptResolved(s, PromptResolution{Text: p.text, Via: "stale", Queued: p.at})
			}
			return
		}
		p.text = text
	}
	if why != "" {
		s.resolveHeld(rt, p, why, held)
		return
	}
	text := strings.ReplaceAll(p.text, "\x1b[201~", "")
	s.Input([]byte("\x1b[200~" + text + "\x1b[201~"))
	go func() {
		time.Sleep(pasteEnterDelay)
		s.Input([]byte("\r"))
	}()
}

// promptStall is how long a queued prompt may wait for a working agent
// before the session logs it, once per prompt (T59: a status file left
// busy by a slash command once held a nudge for minutes). A variable for
// tests.
var promptStall = time.Minute

// logStall logs a queued prompt waiting for a working agent: the sources
// of the state and the last hook event; tm agent explain shows the rest.
func (s *Session) logStall(rt *agentRT, st agent.Merged, waited time.Duration) {
	last := "no hook event yet"
	if ev := rt.tr.Explain().Events; len(ev) > 0 {
		e := ev[len(ev)-1]
		last = fmt.Sprintf("last hook event %s at %s", e.Event, e.At.Format(time.TimeOnly))
	}
	s.cfg.Logf("session %s: queued prompt waiting %s: agent %s (%s), %s; see tm agent explain %s",
		s.cfg.ID, waited.Round(time.Second), st.State, st.Sources, last, s.cfg.ID)
}

func (rt *agentRT) promptHold() time.Duration {
	if rt.cfg.PromptHold > 0 {
		return rt.cfg.PromptHold
	}
	return DefaultPromptHold
}

// resolveHeld sends a prompt held for PromptHold through the agent's
// channel when the prompt allows it, else drops it, and tells the owner.
func (s *Session) resolveHeld(rt *agentRT, p queuedPrompt, why string, held time.Duration) {
	res := PromptResolution{Text: p.text, Via: "dropped", Why: why, Held: held, Queued: p.at}
	if p.channel {
		if res.Err = rt.channelGone(); res.Err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			res.Err = rt.a.Prompt(ctx, s.promptTarget(rt), p.text)
			cancel()
		}
		if res.Err == nil {
			res.Via = "sent"
		}
	}
	s.cfg.Logf("session %s: queued prompt held %s (%s): %s", s.cfg.ID, held.Round(time.Second), why, res.Via)
	if rt.cfg.OnPromptResolved != nil {
		rt.cfg.OnPromptResolved(s, res)
	}
}

// identifyLoop gives a shell session agent state while an agent the user
// started by hand is its foreground job (docs/SPEC.md §8.1 Identify).
func (s *Session) identifyLoop() {
	tick := time.NewTicker(identifyEvery)
	defer tick.Stop()
	var cur int
	for {
		select {
		case <-s.done:
			return
		case <-tick.C:
		}
		pgrp, err := pty.Foreground(s.ptmx)
		if err != nil || pgrp == s.cmd.Process.Pid || pgrp <= 0 {
			pgrp = 0
		}
		if pgrp == cur {
			continue
		}
		cur = pgrp
		s.mu.Lock()
		old := s.ag
		if old != nil && old.observed {
			s.ag = nil
		}
		s.mu.Unlock()
		if old != nil && old.observed {
			old.close()
			s.cfg.Logf("session %s: %s left the foreground", s.cfg.ID, old.a.Name())
			s.notifyState()
		}
		if pgrp == 0 {
			continue
		}
		argv, err := pty.ProcArgs(pgrp)
		if err != nil {
			continue
		}
		a := s.cfg.Identify(agent.ProcessInfo{Argv: agent.UnwrapArgv(argv)})
		if a == nil {
			continue
		}
		cfg := s.cfg.ObservedAgent
		cfg.Agent = a
		rt, err := newAgentRT(cfg, pgrp, true)
		if err != nil {
			s.cfg.Logf("session %s: %v", s.cfg.ID, err)
			continue
		}
		s.mu.Lock()
		if s.ag != nil || s.term == nil {
			s.mu.Unlock()
			continue
		}
		s.ag = rt
		s.mu.Unlock()
		s.cfg.Logf("session %s: identified %s (pid %d) in the foreground", s.cfg.ID, a.Name(), pgrp)
		go s.runAgent(rt)
	}
}
