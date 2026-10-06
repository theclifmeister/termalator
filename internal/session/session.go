package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/theclifmeister/termilator/internal/agent"
	"github.com/theclifmeister/termilator/internal/emu"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/pty"
)

// Config describes a session to start.
type Config struct {
	ID      string
	Role    string
	Project string // the project slug, for coordinator and thread roles
	Thread  string // the thread id, for the thread role
	Argv    []string
	Cwd     string
	Env     []string // the complete environment of the child
	Cols    uint16
	Rows    uint16
	Created time.Time
	// Xtversion is the terminal name reported to XTVERSION queries.
	Xtversion string
	// Scheme is the colour scheme reported to the program until a client
	// reports its own (SetColorScheme); 0 leaves queries unanswered.
	Scheme emu.Scheme
	// Logf receives diagnostics; nil discards them.
	Logf func(format string, args ...any)
	// OnExit is called once, after the process has exited and every
	// subscriber has been closed.
	OnExit func(*Session)
	// RemoteControl: the agent started reachable from another device
	// (docs/SPEC.md §8.2); SetRemoteControl tracks later changes.
	RemoteControl bool
	// RemoteHeld: the user turned remote control off; SetRemoteHeld
	// tracks later changes.
	RemoteHeld bool

	// Agent, when set, makes this an agent session (docs/SPEC.md §8).
	Agent *AgentConfig
	// Identify, when set on a session without Agent, recognises an agent
	// the user starts by hand in it; ObservedAgent configures it then.
	Identify      func(agent.ProcessInfo) agent.Agent
	ObservedAgent AgentConfig
}

// Session is one process on a PTY with the authoritative emulator of its
// screen. All emulator access and every subscriber enqueue happen under mu,
// which is what lets an attach take a snapshot and join the output stream
// at exactly the same byte.
type Session struct {
	cfg  Config
	cmd  *exec.Cmd
	ptmx *os.File
	in   *inputQueue
	done chan struct{}

	mu         sync.Mutex
	term       *emu.Terminal
	subs       map[*Subscriber]struct{}
	cols, rows uint16
	scheme     emu.Scheme
	exitStatus string
	ag         *agentRT
	stateCh    chan struct{} // closed and replaced on every state change

	output atomic.Bool // output arrived since the last screen evaluation
	// sized is set once a console asked for a size (RequestResize): from
	// then on, showing the pane never resizes it (docs/SPEC.md §3.3).
	sized  atomic.Bool
	remote atomic.Bool // remote control is on
	held   atomic.Bool // the user turned it off (RemoteHeld)
	// ident is the role, project and thread set by Adopt; nil until then.
	ident atomic.Pointer[Identity]
	// closeNote, when set, replaces "session exited: …" as the reason
	// subscribers are given when the process ends.
	closeNote string

	// RequestResize's coalescing (rmu before mu).
	rmu         sync.Mutex
	resizedAt   time.Time
	want        [2]uint16 // cols, rows of the request waiting for timer
	resizeTimer *time.Timer
}

// ErrExited is returned for operations on a session whose process is gone.
var ErrExited = errors.New("session has exited")

// Start spawns the process and its emulator.
func Start(cfg Config) (*Session, error) {
	if cfg.Cols == 0 || cfg.Rows == 0 {
		cfg.Cols, cfg.Rows = 80, 24
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Created.IsZero() {
		cfg.Created = time.Now()
	}
	s := &Session{
		cfg:     cfg,
		in:      newInputQueue(),
		done:    make(chan struct{}),
		subs:    map[*Subscriber]struct{}{},
		cols:    cfg.Cols,
		rows:    cfg.Rows,
		scheme:  cfg.Scheme,
		stateCh: make(chan struct{}),
	}
	s.remote.Store(cfg.RemoteControl)
	s.held.Store(cfg.RemoteHeld)
	term, err := emu.NewWith(emu.Options{
		Cols: cfg.Cols, Rows: cfg.Rows,
		// Answers to terminal queries go back to the program. Only this
		// emulator answers; attach mirrors never do.
		WritePty:  func(b []byte) { s.in.push(b, false) },
		Xtversion: cfg.Xtversion,
		// Called from term.Write, with s.mu held.
		ColorScheme: func() (emu.Scheme, bool) { return s.scheme, s.scheme != 0 },
	})
	if err != nil {
		return nil, err
	}
	s.term = term
	cmd, ptmx, err := pty.Start(cfg.Argv, cfg.Cwd, cfg.Env, cfg.Cols, cfg.Rows)
	if err != nil {
		term.Close()
		return nil, err
	}
	s.cmd, s.ptmx = cmd, ptmx
	if cfg.Agent != nil {
		rt, err := newAgentRT(*cfg.Agent, cmd.Process.Pid, false)
		if err != nil {
			// Unreachable for a validated manifest; the process still runs.
			cfg.Logf("session %s: agent: %v", cfg.ID, err)
		} else {
			s.ag = rt
		}
	}
	readDone := make(chan struct{})
	go s.writeLoop()
	go s.readLoop(readDone)
	go s.waitLoop(readDone)
	if s.ag != nil {
		go s.runAgent(s.ag)
	} else if cfg.Identify != nil {
		go s.identifyLoop()
	}
	return s, nil
}

// StateChanged returns a channel that is closed at the next change of
// the agent state (or the session's exit).
func (s *Session) StateChanged() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateCh
}

func (s *Session) notifyState() {
	s.mu.Lock()
	close(s.stateCh)
	s.stateCh = make(chan struct{})
	s.mu.Unlock()
}

// ID returns the session id.
func (s *Session) ID() string { return s.cfg.ID }

// PID returns the process id of the session's process (its process group
// and session leader).
func (s *Session) PID() int { return s.cmd.Process.Pid }

// Done is closed once the process has exited and the session is torn down.
func (s *Session) Done() <-chan struct{} { return s.done }

// SetRemoteControl records whether remote control is on.
func (s *Session) SetRemoteControl(on bool) { s.remote.Store(on) }

// SetRemoteHeld records whether the user turned remote control off.
func (s *Session) SetRemoteHeld(held bool) { s.held.Store(held) }

// Identity is a session's role, project and thread.
type Identity struct{ Role, Project, Thread string }

// Adopt gives the running session another role, project and thread
// (docs/SPEC.md §9, Adopt): Config and Info report it from now on. The
// process's own environment stays as it was started.
func (s *Session) Adopt(id Identity) { s.ident.Store(&id) }

// SetCloseNote sets the reason attached clients are given when the
// process ends, e.g. proto.ClosedRestarting.
func (s *Session) SetCloseNote(note string) {
	s.mu.Lock()
	s.closeNote = note
	s.mu.Unlock()
}

// ExitStatus describes how the process ended; it is empty while it runs.
func (s *Session) ExitStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitStatus
}

// Config returns the configuration the session was started with, with
// the identity Adopt gave it.
func (s *Session) Config() Config {
	c := s.cfg
	if id := s.ident.Load(); id != nil {
		c.Role, c.Project, c.Thread = id.Role, id.Project, id.Thread
	}
	return c
}

// Info describes the session for session.list.
func (s *Session) Info() proto.SessionInfo {
	s.mu.Lock()
	cfg := s.Config()
	info := proto.SessionInfo{
		ID:      s.cfg.ID,
		Role:    cfg.Role,
		Project: cfg.Project,
		Thread:  cfg.Thread,
		Argv:    s.cfg.Argv,
		Cwd:     s.cfg.Cwd,
		PID:     s.cmd.Process.Pid,
		Cols:    s.cols,
		Rows:    s.rows,
		Created: s.cfg.Created,
		Clients: len(s.subs),

		RemoteControl: s.remote.Load(),
		RemoteHeld:    s.held.Load(),
	}
	if s.term != nil {
		info.Title = s.term.Title()
	}
	s.mu.Unlock()
	if st, ok := s.AgentState(); ok {
		info.Agent = st.Agent
		info.State, info.Reason, info.StateSources = string(st.State), st.Reason, st.Sources
		info.AgentSID, info.Identified, info.Queued = st.AgentSID, st.Observed, st.Queued
		if st.RemoteKnown {
			info.RemoteControl = st.RemoteControl
		}
		for _, t := range st.Todos {
			info.TodosTotal++
			switch t.Status {
			case agent.TodoCompleted:
				info.TodosDone++
			case agent.TodoInProgress:
				if info.Current == "" {
					info.Current = t.Text
					if t.ActiveText != "" {
						info.Current = t.ActiveText
					}
				}
			}
		}
	}
	return info
}

// Read returns the visible screen as plain text, or with scrollback the
// whole active screen including its history.
func (s *Session) Read(scrollback bool) (proto.SessionReadResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return proto.SessionReadResult{}, ErrExited
	}
	var text string
	var err error
	if scrollback {
		text, err = s.term.PlainText()
	} else {
		text, err = s.term.Screen()
	}
	return proto.SessionReadResult{Text: text, Cols: s.cols, Rows: s.rows}, err
}

// Input queues bytes for the PTY as if typed. It never blocks.
func (s *Session) Input(p []byte) error {
	if !s.in.push(p, true) {
		return ErrExited
	}
	return nil
}

// Resize changes the pane size: the PTY (SIGWINCH) and the emulator
// together, at one point of the output stream. Every subscriber gets a
// FrameResize at exactly that point, so mirrors resize at the same offset.
func (s *Session) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return fmt.Errorf("session: invalid size %d×%d", cols, rows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return ErrExited
	}
	if cols == s.cols && rows == s.rows {
		return nil
	}
	if err := s.term.Resize(cols, rows); err != nil {
		return err
	}
	if err := pty.Resize(s.ptmx, cols, rows); err != nil {
		s.cfg.Logf("session %s: %v", s.cfg.ID, err)
	}
	s.cols, s.rows = cols, rows
	for sub := range s.subs {
		sub.enqueue(proto.FrameResize, proto.Size(cols, rows))
	}
	return nil
}

// ResizeQuiet is how long after a resize RequestResize waits before the
// next one. Requests in between collapse into the last, as in tmux.
var ResizeQuiet = 250 * time.Millisecond

// RequestResize is Resize at most once per ResizeQuiet: a request soon
// after a resize waits until the quiet time is over, and later requests
// replace it. Consoles typed into in turn, or a window being dragged,
// can't storm the program with SIGWINCH (docs/SPEC.md §3.3).
func (s *Session) RequestResize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return fmt.Errorf("session: invalid size %d×%d", cols, rows)
	}
	s.sized.Store(true)
	s.rmu.Lock()
	defer s.rmu.Unlock()
	if s.resizeTimer == nil {
		// Nothing waits: asking for the size it has is no resize, and
		// mustn't start a quiet time (views ask on every claim).
		s.mu.Lock()
		same := cols == s.cols && rows == s.rows
		s.mu.Unlock()
		if same {
			return nil
		}
	}
	if wait := time.Until(s.resizedAt.Add(ResizeQuiet)); wait > 0 {
		s.want = [2]uint16{cols, rows}
		if s.resizeTimer == nil {
			s.resizeTimer = time.AfterFunc(wait, s.resizeLater)
		}
		return nil
	}
	s.resizedAt = time.Now()
	return s.Resize(cols, rows)
}

// Sized says whether a console has sized the session since it started:
// typed into it, resized a window or changed a layout showing it, or was
// the first to show it.
func (s *Session) Sized() bool { return s.sized.Load() }

// resizeLater applies the request that waited for the quiet time.
func (s *Session) resizeLater() {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	s.resizeTimer = nil
	s.resizedAt = time.Now()
	if err := s.Resize(s.want[0], s.want[1]); err != nil && !errors.Is(err, ErrExited) {
		s.cfg.Logf("session %s: resize: %v", s.cfg.ID, err)
	}
}

// SetColorScheme records the colour scheme of the terminal a client is
// attached from. A program that asked for scheme reports (mode 2031) gets
// one when it changes.
func (s *Session) SetColorScheme(scheme emu.Scheme) error {
	if scheme != emu.SchemeDark && scheme != emu.SchemeLight {
		return fmt.Errorf("session: invalid colour scheme %d", scheme)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return ErrExited
	}
	if scheme == s.scheme {
		return nil
	}
	s.scheme = scheme
	if s.term.Modes().ColorSchemeReport {
		s.in.push(emu.SchemeReport(scheme), false)
	}
	return nil
}

// RequestDigest puts a FrameDigest with the emulator's current state
// digest into sub's stream, at this point of the output.
func (s *Session) RequestDigest(sub *Subscriber) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return ErrExited
	}
	d, err := s.term.Digest()
	if err != nil {
		return err
	}
	sub.enqueue(proto.FrameDigest, []byte(d))
	return nil
}

// Stop ends the session: SIGHUP to its process group, then SIGKILL if it
// is still running after grace. It returns once the session is torn down
// or the kill did not help within a few more seconds.
func (s *Session) Stop(grace time.Duration) {
	pid := s.cmd.Process.Pid
	syscall.Kill(-pid, syscall.SIGHUP)
	syscall.Kill(pid, syscall.SIGHUP)
	select {
	case <-s.done:
		return
	case <-time.After(grace):
	}
	s.cfg.Logf("session %s: still running %v after SIGHUP; sending SIGKILL", s.cfg.ID, grace)
	syscall.Kill(-pid, syscall.SIGKILL)
	syscall.Kill(pid, syscall.SIGKILL)
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		s.cfg.Logf("session %s: did not exit after SIGKILL", s.cfg.ID)
	}
}

// coalesceWindow is how long the reader keeps collecting after a read
// before it hands the bytes on. macOS PTYs deliver ~68-byte reads; one
// emulator write and one frame per batch instead of per read keeps a
// firehose cheap (docs/SPEC.md §3.3).
const coalesceWindow = 300 * time.Microsecond

func (s *Session) readLoop(done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 64<<10)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 && err == nil {
			n, err = s.coalesce(buf, n)
		}
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.term.Write(data)
			s.output.Store(true)
			for sub := range s.subs {
				sub.enqueue(proto.FrameOutput, data)
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// coalesce reads more into buf[n:] until the window passes, buf fills or
// nothing more arrives. A deadline error is not an error of the PTY.
func (s *Session) coalesce(buf []byte, n int) (int, error) {
	end := time.Now().Add(coalesceWindow)
	for n < len(buf) {
		if s.ptmx.SetReadDeadline(end) != nil {
			return n, nil // not pollable: no coalescing
		}
		m, err := s.ptmx.Read(buf[n:])
		n += m
		if err != nil {
			s.ptmx.SetReadDeadline(time.Time{})
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return n, nil
			}
			return n, err
		}
	}
	s.ptmx.SetReadDeadline(time.Time{})
	return n, nil
}

func (s *Session) writeLoop() {
	for {
		b, ok := s.in.pop()
		if !ok {
			return
		}
		if _, err := s.ptmx.Write(b); err != nil {
			s.in.close()
			return
		}
	}
}

func (s *Session) waitLoop(readDone <-chan struct{}) {
	status := "exited"
	if err := s.cmd.Wait(); err != nil {
		status = err.Error()
	}
	// Let the reader drain what the process wrote last. A grandchild that
	// still holds the PTY open would keep it from ever seeing EOF, so close
	// the master after a moment.
	select {
	case <-readDone:
	case <-time.After(500 * time.Millisecond):
	}
	s.ptmx.Close()
	<-readDone
	s.in.close()

	s.mu.Lock()
	s.exitStatus = status
	reason := "session exited: " + status
	if s.closeNote != "" {
		reason = s.closeNote
	}
	for sub := range s.subs {
		sub.enqueue(proto.FrameClosed, []byte(reason))
		sub.close()
	}
	s.subs = map[*Subscriber]struct{}{}
	s.term.Close()
	s.term = nil
	rt := s.ag
	s.mu.Unlock()
	s.cfg.Logf("session %s: pid %d %s", s.cfg.ID, s.cmd.Process.Pid, status)
	if rt != nil {
		rt.tr.Exited(status)
		if rt.observed {
			rt.close()
		} else {
			s.agentChanged(rt)
		}
	}
	close(s.done)
	s.notifyState()
	if s.cfg.OnExit != nil {
		s.cfg.OnExit(s)
	}
}

// inputQueue serialises writes to the PTY without ever blocking the
// caller: neither a client's input nor the emulator's query answers (which
// arrive under Session.mu) may stall on a program that stopped reading.
type inputQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

// maxInput bounds queued client input; query answers are always queued.
const maxInput = 1 << 20

func newInputQueue() *inputQueue {
	q := &inputQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *inputQueue) push(p []byte, bounded bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	if bounded && len(q.buf)+len(p) > maxInput {
		return true // the program is not reading; drop rather than grow
	}
	q.buf = append(q.buf, p...)
	q.cond.Signal()
	return true
}

func (q *inputQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.buf) == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed {
		return nil, false
	}
	b := q.buf
	q.buf = nil
	return b, true
}

func (q *inputQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}
