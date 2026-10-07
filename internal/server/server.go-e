package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/emu"
	"github.com/theclifmeister/terminatr/internal/keychain"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/session"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/ticker"
	"github.com/theclifmeister/terminatr/internal/version"
)

// StopGrace is how long sessions get between SIGHUP and SIGKILL.
const StopGrace = 5 * time.Second

// handlerGrace bounds how long Run waits, after closing every
// connection, for their goroutines to end.
const handlerGrace = 5 * time.Second

// handshakeTimeout bounds how long a new connection may take to say hello.
const handshakeTimeout = 5 * time.Second

// AlreadyRunningError is returned by Run when another server holds the lock.
type AlreadyRunningError struct{ PID int }

func (e *AlreadyRunningError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("already running (pid %d)", e.PID)
	}
	return "already running"
}

// Options configure a server.
type Options struct {
	Paths Paths
	Log   *log.Logger
	// Bin is the absolute path of tm, exported to sessions as TERMINATR_BIN.
	Bin string
	// Exec, when set, runs the pinned binary in place of this process
	// (syscall.Exec), with Args, so the server itself runs from the pin
	// (pinBinary). Nil keeps running the binary started.
	Exec func(bin string, argv, env []string) error
	// Args are the arguments Exec passes, after the binary.
	Args []string
	// Env is the base environment for sessions; nil means os.Environ().
	Env []string
	// RunCLI runs a project command for cli.run (set by package cli,
	// which imports this one). Nil refuses cli.run.
	RunCLI func(p proto.CLIRunParams, c caller.Caller) proto.CLIRunResult
}

// Server owns every session and the control socket.
type Server struct {
	opts    Options
	log     *log.Logger
	build   string
	started time.Time
	stopReq chan struct{}
	stopOne sync.Once

	// threadMu serialises the mirroring of thread state into files.
	threadMu sync.Mutex
	// taskOf caches each thread's task ref by "<project>/<thread>"
	// (taskRef); a thread's task never changes.
	taskOf sync.Map
	// ctxOf is each session's latest context use (setContext), by id.
	ctxOf sync.Map

	mu       sync.Mutex
	sessions map[string]*session.Session
	records  map[string]SessionRecord
	// relaunch marks sessions stopped to be resumed at once under the
	// same id (a remote control change), instead of ending, with the
	// remote control state they get.
	relaunch map[string]bool
	blocked  map[string]bool // sessions whose agent is blocked, for alerts
	nextID   int
	stopping bool
	prevShut string
	lost     []string
	resumed  []string
	conns    map[net.Conn]struct{}
	// handlers counts the connection goroutines: Run waits for them, so
	// nothing logs or writes state after it returned.
	handlers sync.WaitGroup
	// prevProject maps the previous server's session ids to their
	// project, for the restart inbox items.
	prevProject map[string]string

	// tick is the ticker (docs/SPEC.md §7.5); alerts counts the alerts
	// raised, so clients know when to ring.
	tick   *ticker.Ticker
	alerts atomic.Uint64

	agents    *agent.Registry
	agentErrs []string
	// scheme is the colour scheme a client last reported; new sessions
	// start with it.
	scheme emu.Scheme

	// views are what consoles show (views.go).
	views *views
	// watch wakes the session watches (watch.go).
	watch watchers
	// versions caches agent versions for the mod guard (mods.go).
	versions versions
	// asks are the sessions' open questions (ask.go).
	asks asks
	// mods are the sessions' mod listeners by session id (modchan.go).
	mods map[string]*http.Server

	// protocol is the protocol the hello claims, and deaf hangs up on
	// every hello: test hooks (testhooks.go).
	protocol int
	deaf     bool
}

// Run runs a server until ctx is cancelled or a client calls server.stop.
// It returns *AlreadyRunningError if another server is running.
func Run(ctx context.Context, opts Options) error {
	p := opts.Paths
	logger := opts.Log
	if logger == nil {
		logger = log.New(os.Stderr, "", log.LstdFlags)
	}
	for _, d := range []string{p.Home, filepath.Dir(p.Log), filepath.Dir(p.Sessions)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	// The run dir holds the socket, the lock and the pid file. Bind before
	// anything else can start: a server that cannot listen must not own
	// processes nobody can reach.
	if err := ensurePrivateDir(p.RunDir); err != nil {
		return err
	}
	lock, err := takeLock(p)
	if err != nil {
		return err
	}
	defer lock.unlock()

	// Run from the pin before anything else: macOS privacy settings know
	// the server (the responsible process of everything it starts) by the
	// path it runs from, which then stays the same across upgrades.
	if opts.Bin != "" {
		if bin, err := pinBinary(p.RunDir, opts.Bin); err != nil {
			logger.Printf("pin %s: %v; an upgrade in place will break attach re-exec and hooks until restart", opts.Bin, err)
		} else {
			if opts.Exec != nil && opts.Bin != bin && !lock.handedOver {
				logger.Printf("running from %s", bin)
				err := execPinned(lock, bin, opts.Args, opts.Exec)
				logger.Printf("exec %s: %v; running from %s", bin, err, opts.Bin)
			}
			opts.Bin = bin
			removeLegacyPins(p.Home)
		}
	}

	// We hold the lock, so any socket file left here is stale.
	os.Remove(p.Socket)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p.Socket, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen %s: %w", p.Socket, err)
	}
	ln.SetUnlinkOnClose(false)
	if err := os.Chmod(p.Socket, 0o600); err != nil {
		ln.Close()
		return err
	}
	if err := os.WriteFile(p.PID, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		ln.Close()
		return err
	}

	s := &Server{
		opts:     opts,
		log:      logger,
		build:    version.BuildID(),
		started:  time.Now(),
		stopReq:  make(chan struct{}),
		sessions: map[string]*session.Session{},
		records:  map[string]SessionRecord{},
		relaunch: map[string]bool{},
		blocked:  map[string]bool{},
		nextID:   1,
		conns:    map[net.Conn]struct{}{},

		prevProject: map[string]string{},
		protocol:    proto.Protocol,
	}
	if n, deaf := testHello(); n > 0 || deaf {
		s.deaf = deaf
		if n > 0 {
			s.protocol = n
		}
		logger.Printf("test hook %s: protocol %d, deaf %v", testHelloEnv, s.protocol, s.deaf)
	}
	s.watchOwner()
	s.views = newViews(s, filepath.Join(filepath.Dir(p.Sessions), "views.json"), logger.Printf)
	s.loadAgents()
	toResume, lost := s.loadPrevious()
	if err := s.saveLocked(""); err != nil {
		logger.Printf("sessions.json: %v", err)
	}
	logger.Printf("server pid %d %s protocol %d listening on %s", os.Getpid(), s.build, proto.Protocol, p.Socket)
	go func() {
		if st := keychain.Probe(runtime.GOOS, os.Getenv, keychain.Run); st.Checked && !st.OK {
			logger.Printf("keychain: %s; gh and git push over https will fail in sessions: %s", st.Detail, keychain.Fix)
		}
	}()

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			s.handlers.Add(1)
			go func() {
				defer s.handlers.Done()
				s.handle(c)
			}()
		}
	}()
	// Resume once hooks can be answered: a resumed agent fires
	// SessionStart at once.
	if outs := s.resume(toResume); s.prevShut != "" {
		s.logRestart(s.prevShut, append(lost, outs...))
	}
	// The views come back with the sessions that did.
	s.views.load()
	tctx, stopTicker := context.WithCancel(context.Background())
	tickerDone := s.startTicker(tctx)

	select {
	case <-ctx.Done():
		logger.Printf("stopping: %v", context.Cause(ctx))
	case <-s.stopReq:
		logger.Printf("stopping: requested by a client")
	}
	// Let a sweep in progress finish while the socket still answers.
	stopTicker()
	select {
	case <-tickerDone:
	case <-time.After(15 * time.Second):
		logger.Printf("ticker: still sweeping; stopping anyway")
	}
	ln.Close()
	<-acceptDone
	s.shutdown()
	handled := make(chan struct{})
	go func() {
		s.handlers.Wait()
		close(handled)
	}()
	select {
	case <-handled:
	case <-time.After(handlerGrace):
		logger.Printf("connections still closing; stopping anyway")
	}
	os.Remove(p.Socket)
	os.Remove(p.PID)
	logger.Printf("server stopped")
	return nil
}

// loadPrevious reads the last server's sessions.json: was its shutdown
// clean, which agent sessions to resume, and which sessions are gone.
// Shell sessions and agents without a recorded agent session id are never
// restored (docs/SPEC.md §3.6).
func (s *Server) loadPrevious() (resume []SessionRecord, lost []restartOutcome) {
	prev, err := loadState(s.opts.Paths.Sessions)
	if err != nil {
		s.log.Printf("sessions.json unreadable, starting fresh: %v", err)
		return nil, nil
	}
	if prev == nil {
		return nil, nil
	}
	if prev.NextID > s.nextID {
		s.nextID = prev.NextID
	}
	if prev.Shutdown == "clean" {
		s.prevShut = "clean"
	} else {
		s.prevShut = "crash"
		s.log.Printf("previous server (pid %d) did not shut down cleanly", prev.ServerPID)
	}
	for _, r := range prev.Sessions {
		if r.Project != "" {
			s.prevProject[r.ID] = r.Project
		}
		if r.Agent != "" && r.AgentSessionID != "" {
			resume = append(resume, r)
			continue
		}
		s.lost = append(s.lost, r.ID)
		lost = append(lost, restartOutcome{rec: r, how: "lost"})
	}
	if len(s.lost) > 0 {
		s.log.Printf("sessions of the previous server not restored: %v", s.lost)
	}
	return resume, lost
}

// saveLocked rewrites sessions.json; s.mu held (or no concurrency yet).
func (s *Server) saveLocked(shutdown string) error {
	st := &State{
		Version:   stateVersion,
		ServerPID: os.Getpid(),
		Started:   s.started,
		Shutdown:  shutdown,
		NextID:    s.nextID,
	}
	for _, r := range s.records {
		if shutdown == "clean" {
			r.CleanExit = true
		}
		st.Sessions = append(st.Sessions, r)
	}
	sort.Slice(st.Sessions, func(i, j int) bool { return st.Sessions[i].Created.Before(st.Sessions[j].Created) })
	return saveState(s.opts.Paths.Sessions, st)
}

func (s *Server) requestStop() { s.stopOne.Do(func() { close(s.stopReq) }) }

// shutdown stops every session (SIGHUP, then SIGKILL after StopGrace),
// records them for resume and marks the shutdown clean.
func (s *Server) shutdown() {
	s.mu.Lock()
	s.stopping = true
	sessions := make([]*session.Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, sess := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess.Stop(StopGrace)
		}()
	}
	wg.Wait()
	s.closeMods()

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveLocked("clean"); err != nil {
		s.log.Printf("sessions.json: %v", err)
	}
	os.RemoveAll(filepath.Join(s.opts.Paths.RunDir, "s"))
}

// handle runs one connection: peer check, handshake, then control or
// attach.
func (s *Server) handle(c *net.UnixConn) {
	defer c.Close()
	uid, pid, err := peerCred(c)
	if err != nil {
		s.log.Printf("peer credentials: %v", err)
		return
	}
	if uid != os.Getuid() {
		s.log.Printf("rejected connection from uid %d (pid %d)", uid, pid)
		return
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()

	br := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(handshakeTimeout))
	var hello proto.Hello
	if err := readJSONLine(br, &hello); err != nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	if s.deaf {
		return
	}
	reply := s.hello()
	if err := writeJSONLine(c, reply); err != nil {
		return
	}
	// Both sides check; the client says what went wrong, the server just
	// refuses to go on (docs/SPEC.md §3.3).
	if err := proto.Check(hello, reply); err != nil {
		s.log.Printf("refused %s connection from pid %d: %v", hello.Kind, pid, err)
		return
	}
	switch hello.Kind {
	case proto.KindControl, proto.KindHook:
		s.serveControl(c, br, pid)
	case proto.KindAttach:
		s.serveAttach(c, br)
	default:
		s.log.Printf("refused connection of unknown kind %q from pid %d", hello.Kind, pid)
	}
}

// hello is the server's side of the handshake.
func (s *Server) hello() proto.Hello {
	return proto.Hello{
		Protocol: s.protocol,
		Version:  version.Version,
		Build:    s.build,
		Bin:      s.opts.Bin,
		PID:      os.Getpid(),
	}
}

func (s *Server) serveControl(c net.Conn, br *bufio.Reader, peerPID int) {
	for {
		var req proto.Request
		if err := readJSONLine(br, &req); err != nil {
			return
		}
		switch req.Method {
		case proto.MethodViewSubscribe:
			s.serveViewStream(c, br, req)
			return
		case proto.MethodSessionWatch:
			s.serveWatch(c, br, req)
			return
		case proto.MethodProjectWatch:
			s.serveProjectWatch(c, br, req)
			return
		case proto.MethodSessionAsk:
			s.serveAsk(c, br, req)
			return
		}
		result, perr := s.dispatch(req, peerPID)
		resp := proto.Response{ID: req.ID, Error: perr}
		if perr == nil {
			b, err := json.Marshal(result)
			if err != nil {
				resp.Error = proto.Errorf(proto.ErrInternal, "%v", err)
			} else {
				resp.Result = b
			}
		}
		if err := writeJSONLine(c, resp); err != nil {
			return
		}
		if req.Method == proto.MethodServerStop && perr == nil {
			s.log.Printf("server.stop from pid %d", peerPID)
			s.requestStop()
		}
	}
}

func (s *Server) dispatch(req proto.Request, peerPID int) (any, *proto.Error) {
	if strings.HasPrefix(req.Method, "view.") {
		var p proto.ViewParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.views.do(req.Method, p)
	}
	switch req.Method {
	case proto.MethodCallerWho:
		return s.whoIs(peerPID), nil
	case proto.MethodCLIRun:
		var p proto.CLIRunParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		res, perr := s.cliRun(p, peerPID)
		s.kick()
		return res, perr
	case proto.MethodProjectRename:
		var p proto.ProjectRenameParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.renameProject(p, s.callerOf(peerPID))
	case proto.MethodPing:
		return map[string]bool{"pong": true}, nil
	case proto.MethodServerStatus:
		return s.status(), nil
	case proto.MethodServerKeychain:
		return keychain.Probe(runtime.GOOS, os.Getenv, keychain.Run), nil
	case proto.MethodServerStop:
		var p proto.ServerStopParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.stop(p)
	case proto.MethodSessionList:
		return s.list(), nil
	case proto.MethodSessionStart:
		var p proto.SessionStartParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.startSession(p)
	case proto.MethodSessionStop:
		var p proto.SessionIDParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.stopSession(p.ID)
	case proto.MethodSessionRead:
		var p proto.SessionReadParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		sess, perr := s.session(p.ID)
		if perr != nil {
			return nil, perr
		}
		r, err := sess.Read(p.Scrollback)
		if err != nil {
			return nil, sessionError(p.ID, err)
		}
		return r, nil
	case proto.MethodSessionKeys:
		var p proto.SessionKeysParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		sess, perr := s.session(p.ID)
		if perr != nil {
			return nil, perr
		}
		if err := sess.Input([]byte(p.Data)); err != nil {
			return nil, sessionError(p.ID, err)
		}
		return struct{}{}, nil
	case proto.MethodSessionPrompt:
		var p proto.SessionPromptParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.prompt(p)
	case proto.MethodSessionWait:
		var p proto.SessionWaitParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.wait(p)
	case proto.MethodHookEvent:
		var p proto.HookEventParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.hookEvent(p), nil
	case proto.MethodSessionAnswer:
		var p proto.SessionAnswerParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if _, perr := s.session(p.ID); perr != nil {
			return nil, perr
		}
		res, perr := s.asks.answer(p.ID, p.Index, p.Answer)
		s.watch.wake()
		return res, perr
	case proto.MethodSessionRemote:
		var p proto.SessionRemoteParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.remote(p)
	case proto.MethodSessionAdopt:
		var p proto.SessionAdoptParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.adopt(p)
	case proto.MethodAgentList:
		return s.agentList(), nil
	case proto.MethodAgentReload:
		s.loadAgents()
		return s.agentList(), nil
	case proto.MethodAgentExplain:
		var p proto.SessionIDParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return s.explain(p.ID)
	}
	return nil, proto.Errorf(proto.ErrUnknownMethod, "unknown method %q", req.Method)
}

func (s *Server) status() proto.ServerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.ServerStatus{
		PID:              os.Getpid(),
		Version:          version.Version,
		Build:            s.build,
		Protocol:         proto.Protocol,
		Started:          s.started,
		Sessions:         len(s.sessions),
		Socket:           s.opts.Paths.Socket,
		Home:             s.opts.Paths.Home,
		PreviousShutdown: s.prevShut,
		Lost:             s.lost,
		Resumed:          s.resumed,
	}
}

func (s *Server) stop(p proto.ServerStopParams) (any, *proto.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agents := 0
	for id, sess := range s.sessions {
		if sess.Config().Role != proto.RoleShell || s.records[id].Agent != "" {
			agents++
		}
	}
	if agents > 0 && !p.Yes {
		return nil, proto.Errorf(proto.ErrRefused, "%d agent session(s) running; pass --yes to stop them", agents)
	}
	return map[string]int{"sessions": len(s.sessions)}, nil
}

func (s *Server) list() proto.SessionListResult {
	s.mu.Lock()
	sessions := make([]*session.Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	res := proto.SessionListResult{Sessions: []proto.SessionInfo{}, Alerts: s.alerts.Load()}
	for _, sess := range sessions {
		res.Sessions = append(res.Sessions, s.info(sess))
	}
	sort.Slice(res.Sessions, func(i, j int) bool { return res.Sessions[i].Created.Before(res.Sessions[j].Created) })
	return res
}

// info is sess's info with its thread's task.
func (s *Server) info(sess *session.Session) proto.SessionInfo {
	info := sess.Info()
	info.Question = s.asks.of(info.ID)
	if v, ok := s.ctxOf.Load(info.ID); ok {
		c := v.(ctxUse)
		info.Context, info.ContextWindow = c.tokens, c.window
	}
	if info.Role == proto.RoleThread && thread.ValidID(info.Thread) {
		info.Task = s.taskRef(info.Project, info.Thread)
	}
	return info
}

// taskRef is thread id's task ref in project slug, "" for none.
func (s *Server) taskRef(slug, id string) string {
	key := slug + "/" + id
	if v, ok := s.taskOf.Load(key); ok {
		return v.(string)
	}
	p, err := project.Open(slug)
	if err != nil {
		return ""
	}
	rec, err := thread.Load(p, id)
	if err != nil {
		return ""
	}
	s.taskOf.Store(key, rec.Task)
	return rec.Task
}

func (s *Server) session(id string) (*session.Session, *proto.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, proto.Errorf(proto.ErrUnknownSession, "no session %q", id)
	}
	return sess, nil
}

func sessionError(id string, err error) *proto.Error {
	if errors.Is(err, session.ErrExited) {
		return proto.Errorf(proto.ErrUnknownSession, "session %q has exited", id)
	}
	return proto.Errorf(proto.ErrInternal, "%v", err)
}

func (s *Server) startSession(p proto.SessionStartParams) (any, *proto.Error) {
	argv := p.Argv
	if len(argv) == 0 && p.Agent == "" {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		argv = []string{sh, "-l"}
	}
	if p.Agent != "" && len(argv) > 0 {
		return nil, proto.Errorf(proto.ErrBadParams, "pass an agent or a command, not both")
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	if !filepath.IsAbs(cwd) {
		return nil, proto.Errorf(proto.ErrBadParams, "cwd must be absolute: %q", cwd)
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return nil, proto.Errorf(proto.ErrBadParams, "cwd is not a directory: %s", cwd)
	}
	role := p.Role
	switch role {
	case "":
		role = proto.RoleShell
	case proto.RoleShell:
	case proto.RoleCoordinator, proto.RoleThread:
		if p.Agent == "" || p.Project == "" {
			return nil, proto.Errorf(proto.ErrBadParams, "role %s needs an agent and a project", role)
		}
		if role == proto.RoleThread && p.Thread == "" {
			return nil, proto.Errorf(proto.ErrBadParams, "role thread needs a thread id")
		}
	default:
		return nil, proto.Errorf(proto.ErrBadParams, "unknown role %q", role)
	}
	if p.Brief != "" && !filepath.IsAbs(p.Brief) {
		return nil, proto.Errorf(proto.ErrBadParams, "brief must be an absolute path: %q", p.Brief)
	}
	cols, rows := p.Cols, p.Rows
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil, proto.Errorf(proto.ErrRefused, "server is stopping")
	}
	id := fmt.Sprintf("s-%d", s.nextID)
	s.nextID++
	created := time.Now()
	rec := SessionRecord{ID: id, Role: role, Project: p.Project, Thread: p.Thread, Argv: argv, Cwd: cwd,
		Created: created, Cols: cols, Rows: rows}
	if p.Agent != "" {
		rec.Agent, rec.AgentSessionID, rec.Brief, rec.Model, rec.Yolo = p.Agent, newUUID(), p.Brief, p.Model, p.Yolo
		rec.Kickoff, rec.RemoteControl = p.Kickoff, p.RemoteControl
		if p.RemoteControl && role != proto.RoleCoordinator {
			return nil, proto.Errorf(proto.ErrRefused, "remote control is for coordinators; threads are reached through theirs")
		}
		if p.RemoteControl && agent.RemoteControlOf(s.agentOr(p.Agent)) == nil {
			// The project's setting asks for it, but this agent has
			// none: start without it rather than not at all.
			s.log.Printf("agent %s has no remote control; starting %s without it", p.Agent, id)
			rec.RemoteControl = false
		}
		l := agentLaunch{rec: rec, kick: p.Kickoff, cols: cols, rows: rows}
		if p.ResumeSID != "" {
			l.rec.AgentSessionID, l.rec.Prompted, l.resume, l.kick = p.ResumeSID, true, true, ""
		}
		sess, perr := s.launchAgent(l)
		if perr != nil {
			return nil, perr
		}
		return proto.SessionStartResult{Session: s.info(sess)}, nil
	}
	env := sessionEnv(s.baseEnv(), s.terminatrEnv(rec))
	home, _ := os.UserHomeDir()
	reg := s.agents
	cfg := session.Config{
		ID: id, Role: role, Argv: argv, Cwd: cwd, Env: env,
		Cols: cols, Rows: rows, Created: created,
		Xtversion: "terminatr " + version.Version,
		Scheme:    s.scheme,
		Logf:      s.log.Printf,
		OnExit:    s.sessionExited,
		// A shell gets agent state while an agent the user started by
		// hand runs in its foreground (docs/SPEC.md §8.1 Identify).
		ObservedAgent: session.AgentConfig{Home: home, OnChange: s.agentChanged,
			PromptHold: envDuration(envPromptHold), OnPromptResolved: s.promptResolved},
	}
	if reg != nil {
		cfg.Identify = func(pi agent.ProcessInfo) agent.Agent {
			a, _ := reg.Identify(pi)
			return a
		}
	}
	sess, err := session.Start(cfg)
	if err != nil {
		return nil, proto.Errorf(proto.ErrRefused, "%v", err)
	}
	s.sessions[id] = sess
	s.records[id] = rec
	if err := s.saveLocked(""); err != nil {
		s.log.Printf("sessions.json: %v", err)
	}
	s.log.Printf("session %s: started pid %d %q in %s", id, sess.PID(), argv, cwd)
	return proto.SessionStartResult{Session: s.info(sess)}, nil
}

func (s *Server) sessionExited(sess *session.Session) {
	s.mu.Lock()
	// The views keep the panes of a stopping server, and of a session
	// relaunched under its id: they come back with it.
	gone := !s.stopping
	defer func() {
		s.mu.Unlock()
		if gone {
			s.views.sessionGone(sess.ID())
		}
	}()
	delete(s.sessions, sess.ID())
	delete(s.blocked, sess.ID())
	s.closeModLocked(sess.ID())
	if s.tick != nil {
		s.tick.Kick()
	}
	s.watch.wake()
	if s.stopping {
		return // keep the record: shutdown writes it for resume
	}
	if on, ok := s.relaunch[sess.ID()]; ok {
		delete(s.relaunch, sess.ID())
		gone = !s.relaunchLocked(sess, on)
		return
	}
	os.RemoveAll(s.runtimeDir(sess.ID()))
	delete(s.records, sess.ID())
	if err := s.saveLocked(""); err != nil {
		s.log.Printf("sessions.json: %v", err)
	}
}

func (s *Server) stopSession(id string) (any, *proto.Error) {
	sess, perr := s.session(id)
	if perr != nil {
		return nil, perr
	}
	sess.Stop(StopGrace)
	return map[string]string{"status": sess.ExitStatus()}, nil
}

// serveAttach streams one session to a client: the snapshot, then output
// and resizes in order, while it reads the client's input and requests.
// The client leaving (DETACH, EOF, a dead socket) changes nothing else.
func (s *Server) serveAttach(c net.Conn, br *bufio.Reader) {
	c.SetReadDeadline(time.Now().Add(handshakeTimeout))
	var req proto.AttachRequest
	if err := readJSONLine(br, &req); err != nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	sess, perr := s.session(req.Attach.Session)
	if perr != nil {
		writeJSONLine(c, proto.AttachReply{Error: perr})
		return
	}
	sub, err := sess.Attach()
	if err != nil {
		writeJSONLine(c, proto.AttachReply{Error: sessionError(req.Attach.Session, err)})
		return
	}
	info := s.info(sess)
	if err := writeJSONLine(c, proto.AttachReply{Attached: &info}); err != nil {
		sub.Detach()
		return
	}
	s.log.Printf("session %s: client attached", sess.ID())

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer c.Close()
		for {
			b, ok := sub.Next()
			if !ok {
				return
			}
			if _, err := c.Write(b); err != nil {
				return
			}
		}
	}()

	reason := "eof"
	var buf []byte
loop:
	for {
		typ, payload, err := proto.ReadFrame(br, buf)
		if err != nil {
			break
		}
		buf = payload
		switch typ {
		case proto.FrameInput:
			sess.Input(payload)
		case proto.FrameDigestReq:
			sess.RequestDigest(sub)
		case proto.FrameColorScheme:
			if len(payload) == 1 {
				scheme := emu.Scheme(payload[0])
				if err := sess.SetColorScheme(scheme); err == nil {
					s.mu.Lock()
					s.scheme = scheme
					s.mu.Unlock()
				}
			}
		case proto.FrameDetach:
			reason = "detach"
			break loop
		}
	}
	sub.Detach()
	<-writerDone
	s.log.Printf("session %s: client left (%s, %d resyncs)", sess.ID(), reason, sub.Resyncs())
}

func decodeParams(raw json.RawMessage, v any) *proto.Error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return proto.Errorf(proto.ErrBadParams, "%v", err)
	}
	return nil
}

// maxLine bounds one NDJSON line.
const maxLine = 16 << 20

func readJSONLine(br *bufio.Reader, v any) error {
	var line []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return err
		}
		line = append(line, chunk...)
		if len(line) > maxLine {
			return fmt.Errorf("line too long")
		}
		if !isPrefix {
			break
		}
	}
	return json.Unmarshal(line, v)
}

func writeJSONLine(w net.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
