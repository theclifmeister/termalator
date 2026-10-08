package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/emu"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/version"
)

// testPaths gives a test its own home and a short socket path (a macOS
// temp dir is too long for sun_path).
func testPaths(t *testing.T) Paths {
	t.Helper()
	home := t.TempDir()
	sockDir, err := os.MkdirTemp("/tmp", "tmt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	t.Setenv("TERMINATR_HOME", home)
	t.Setenv("TERMINATR_SOCKET", filepath.Join(sockDir, "tm.sock"))
	t.Setenv("TERMINATR_LAUNCHD", "off")
	p, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type running struct {
	cancel context.CancelFunc
	done   chan error
}

func startServer(t *testing.T, p Paths) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan error, 1)}
	w := &testWriter{t: t}
	// Registered first, so it runs last: after the server stopped.
	t.Cleanup(w.mute)
	go func() {
		r.done <- Run(ctx, Options{
			Paths: p,
			Log:   log.New(w, "server: ", log.Lmicroseconds),
			Bin:   "/nonexistent/tm",
			Env:   []string{"PATH=/usr/bin:/bin", "PS1=$ ", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=x", "TMUX=/tmp/x", "KEEP=me"},
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := Dial(p, proto.KindControl)
		if err == nil {
			c.Close()
			break
		}
		// A test hook may make the server refuse this hello, or every one.
		var mm *proto.MismatchError
		if errors.As(err, &mm) {
			break
		}
		if raw, err := net.Dial("unix", p.Socket); err == nil && os.Getenv(testHelloEnv) == "deaf" {
			raw.Close()
			break
		}
		select {
		case err := <-r.done:
			t.Fatalf("server exited: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { r.stop(t) })
	return r
}

func (r *running) stop(t *testing.T) {
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		r.done <- nil // let a second stop return at once
	case <-time.After(15 * time.Second):
		t.Error("server did not stop")
	}
}

// testWriter logs the server through t until the test ends. Run returns
// before every connection goroutine has, and a line logged after the test
// completed panics (e.g. "client c-2 left" after TestViewSubscribeStream).
type testWriter struct {
	t     *testing.T
	mu    sync.Mutex
	muted bool
}

func (w *testWriter) mute() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.muted = true
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.muted {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

func call(t *testing.T, c *Client, method string, params, result any) {
	t.Helper()
	if err := c.Call(method, params, result); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestControlSessionLifecycle(t *testing.T) {
	p := testPaths(t)
	r := startServer(t, p)
	c, err := Connect(p, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var st proto.ServerStatus
	call(t, c, proto.MethodServerStatus, nil, &st)
	if st.PID != os.Getpid() || st.Protocol != proto.Protocol || st.Sessions != 0 || st.Build != version.BuildID() {
		t.Fatalf("status %+v", st)
	}

	var started proto.SessionStartResult
	call(t, c, proto.MethodSessionStart, proto.SessionStartParams{
		Argv: []string{"/bin/sh"}, Cwd: t.TempDir(), Cols: 90, Rows: 20,
	}, &started)
	id := started.Session.ID
	if id != "s-1" || started.Session.Cols != 90 {
		t.Fatalf("started %+v", started.Session)
	}

	call(t, c, proto.MethodSessionKeys, proto.SessionKeysParams{ID: id,
		Data: "echo id=$TERMINATR_SESSION cc=${CLAUDECODE:-none}${CLAUDE_CODE_SESSION_ID:-none} tmux=${TMUX:-none} keep=$KEEP term=$TERM\r"}, nil)
	want := "id=s-1 cc=nonenone tmux=none keep=me term=xterm-256color"
	eventually(t, "the echo", func() bool {
		var rr proto.SessionReadResult
		call(t, c, proto.MethodSessionRead, proto.SessionReadParams{ID: id}, &rr)
		return strings.Contains(rr.Text, want)
	})

	var list proto.SessionListResult
	call(t, c, proto.MethodSessionList, nil, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].ID != id {
		t.Fatalf("list %+v", list)
	}
	st0, _ := loadState(p.Sessions)
	if len(st0.Sessions) != 1 || st0.Shutdown != "" {
		t.Fatalf("sessions.json while running: %+v", st0)
	}

	call(t, c, proto.MethodSessionStop, proto.SessionIDParams{ID: id}, nil)
	err = c.Call(proto.MethodSessionRead, proto.SessionReadParams{ID: id}, nil)
	var perr *proto.Error
	if !errors.As(err, &perr) || perr.Code != proto.ErrUnknownSession {
		t.Fatalf("read after stop: %v", err)
	}
	if err := c.Call("no.such.method", nil, nil); !errors.As(err, &perr) || perr.Code != proto.ErrUnknownMethod {
		t.Fatalf("unknown method: %v", err)
	}
	err = c.Call(proto.MethodSessionStart, proto.SessionStartParams{Cwd: "relative"}, nil)
	if !errors.As(err, &perr) || perr.Code != proto.ErrBadParams {
		t.Fatalf("relative cwd: %v", err)
	}

	// A session left running is recorded at a clean stop.
	call(t, c, proto.MethodSessionStart, proto.SessionStartParams{Argv: []string{"/bin/sh"}, Cwd: "/"}, &started)
	r.stop(t)
	st1, _ := loadState(p.Sessions)
	if st1.Shutdown != "clean" || len(st1.Sessions) != 1 || !st1.Sessions[0].CleanExit || st1.NextID != 3 {
		t.Fatalf("sessions.json after stop: %+v", st1)
	}
	if _, err := os.Stat(p.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left behind: %v", err)
	}
}

func TestServerStopMethod(t *testing.T) {
	p := testPaths(t)
	r := startServer(t, p)
	c, err := Connect(p, false)
	if err != nil {
		t.Fatal(err)
	}
	call(t, c, proto.MethodServerStop, proto.ServerStopParams{}, nil)
	c.Close()
	select {
	case <-r.done:
		r.done <- nil
	case <-time.After(10 * time.Second):
		t.Fatal("server.stop did not stop the server")
	}
	if !WaitStopped(p, time.Second) {
		t.Fatal("lock still held")
	}
}

func TestSecondServerRefused(t *testing.T) {
	p := testPaths(t)
	startServer(t, p)
	err := Run(context.Background(), Options{Paths: p, Log: log.New(io.Discard, "", 0)})
	var running *AlreadyRunningError
	if !errors.As(err, &running) || running.PID != os.Getpid() {
		t.Fatalf("second Run: %v", err)
	}
	// The first server is untouched.
	if c, err := Dial(p, proto.KindControl); err != nil {
		t.Fatalf("first server gone: %v", err)
	} else {
		c.Close()
	}
}

// TestStartDuringLockProbe: a client probing the lock (Connect, WaitStopped)
// holds it for a moment; a server starting just then must wait it out, not
// report "already running" (T28: TestSmokeServerCommands flaked on this).
func TestStartDuringLockProbe(t *testing.T) {
	p := testPaths(t)
	os.MkdirAll(p.RunDir, 0o700)
	lk, err := tryLock(p.Lock)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(200*time.Millisecond, lk.unlock)
	startServer(t, p)
}

// TestLockHeldWithoutServer: a lock held for good by something that wrote
// no pid file is still reported, after lockWait.
func TestLockHeldWithoutServer(t *testing.T) {
	p := testPaths(t)
	os.MkdirAll(p.RunDir, 0o700)
	lk, err := tryLock(p.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.unlock()
	t0 := time.Now()
	err = Run(context.Background(), Options{Paths: p, Log: log.New(io.Discard, "", 0)})
	var running *AlreadyRunningError
	if !errors.As(err, &running) || running.PID != 0 {
		t.Fatalf("Run: %v", err)
	}
	if d := time.Since(t0); d < lockWait {
		t.Fatalf("gave up after %v, want %v", d, lockWait)
	}
}

func TestCrashIsDetected(t *testing.T) {
	p := testPaths(t)
	os.MkdirAll(filepath.Dir(p.Sessions), 0o700)
	// What a crashed server leaves: no clean marker, a stale socket.
	saveState(p.Sessions, &State{Version: 1, ServerPID: 999999, NextID: 7,
		Sessions: []SessionRecord{{ID: "s-6", Role: proto.RoleShell}}})
	ln, err := net.Listen("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	startServer(t, p)
	c, err := Connect(p, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var st proto.ServerStatus
	call(t, c, proto.MethodServerStatus, nil, &st)
	if st.PreviousShutdown != "crash" || len(st.Lost) != 1 || st.Lost[0] != "s-6" {
		t.Fatalf("status %+v", st)
	}
	var started proto.SessionStartResult
	call(t, c, proto.MethodSessionStart, proto.SessionStartParams{Argv: []string{"/bin/sh"}, Cwd: "/"}, &started)
	if started.Session.ID != "s-7" {
		t.Fatalf("ids must not be reused across restarts: got %s", started.Session.ID)
	}
}

func TestStaleSocketWithoutServer(t *testing.T) {
	p := testPaths(t)
	ln, err := net.Listen("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	os.MkdirAll(p.RunDir, 0o700)
	os.WriteFile(p.PID, []byte("999999\n"), 0o600)

	if _, err := Connect(p, false); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Connect: %v", err)
	}
	for _, f := range []string{p.Socket, p.PID} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s not cleaned up", f)
		}
	}
}

func TestHungServerIsReported(t *testing.T) {
	p := testPaths(t)
	os.MkdirAll(p.RunDir, 0o700)
	// A "server" that holds the lock and accepts but never says hello.
	lk, err := tryLock(p.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.unlock()
	os.WriteFile(p.PID, []byte("4242\n"), 0o600)
	ln, err := net.Listen("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	t0 := time.Now()
	_, err = Connect(p, true)
	var unresp *UnresponsiveError
	if !errors.As(err, &unresp) || unresp.PID != 4242 {
		t.Fatalf("Connect: %v", err)
	}
	if d := time.Since(t0); d > 4*time.Second {
		t.Fatalf("took %v", d)
	}
	if !strings.Contains(err.Error(), "tm server stop --force") {
		t.Fatalf("message lacks the hint: %v", err)
	}
}

// rawHello sends a hand-made hello and reports the server's hello and
// whether the server then serves the connection: a control connection
// answers a ping, an attach connection answers an attach request.
func rawHello(t *testing.T, p Paths, h proto.Hello) (proto.Hello, bool) {
	t.Helper()
	conn, err := net.Dial("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	if err := writeJSONLine(conn, h); err != nil {
		t.Fatal(err)
	}
	var reply proto.Hello
	if err := readJSONLine(br, &reply); err != nil {
		t.Fatal(err)
	}
	if h.Kind == proto.KindAttach {
		writeJSONLine(conn, proto.AttachRequest{Attach: proto.AttachParams{Session: "s-404"}})
	} else {
		writeJSONLine(conn, proto.Request{ID: 1, Method: proto.MethodPing})
	}
	var any map[string]any
	return reply, readJSONLine(br, &any) == nil
}

func TestHandshakeVersioning(t *testing.T) {
	p := testPaths(t)
	startServer(t, p)
	build := version.BuildID()
	cases := []struct {
		name string
		h    proto.Hello
		ok   bool
	}{
		{"control same", proto.Hello{Protocol: proto.Protocol, Build: build, Kind: proto.KindControl}, true},
		{"control other build", proto.Hello{Protocol: proto.Protocol, Build: "other", Kind: proto.KindControl}, true},
		{"control newer protocol", proto.Hello{Protocol: proto.Protocol + 1, Build: build, Kind: proto.KindControl}, false},
		{"attach same", proto.Hello{Protocol: proto.Protocol, Build: build, Kind: proto.KindAttach}, true},
		{"attach other build", proto.Hello{Protocol: proto.Protocol, Build: "other", Kind: proto.KindAttach}, false},
		{"unknown kind", proto.Hello{Protocol: proto.Protocol, Build: build, Kind: "bogus"}, false},
	}
	for _, tc := range cases {
		reply, served := rawHello(t, p, tc.h)
		if reply.PID != os.Getpid() || reply.Protocol != proto.Protocol || reply.Build != build || reply.Bin != "/nonexistent/tm" {
			t.Errorf("%s: server hello %+v", tc.name, reply)
		}
		if served != tc.ok {
			t.Errorf("%s: served=%v, want %v", tc.name, served, tc.ok)
		}
	}
	// The client side of Dial reports the mismatch itself.
	var mm *proto.MismatchError
	if err := proto.Check(proto.Hello{Protocol: proto.Protocol, Build: "other", Kind: proto.KindAttach}, mustHello(t, p)); !errors.As(err, &mm) || !mm.ReExec {
		t.Errorf("attach mismatch: %v", err)
	}
}

func mustHello(t *testing.T, p Paths) proto.Hello {
	c, err := Dial(p, proto.KindControl)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.Server
}

// TestAttachStream checks what the attach client (M2) builds on: a
// snapshot, then the ordered stream, input, resize, ping and detach.
func TestAttachStream(t *testing.T) {
	p := testPaths(t)
	startServer(t, p)
	c, err := Connect(p, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var started proto.SessionStartResult
	call(t, c, proto.MethodSessionStart, proto.SessionStartParams{Argv: []string{"/bin/sh"}, Cwd: "/", Cols: 80, Rows: 24}, &started)
	id := started.Session.ID

	if _, _, err := Attach(p, proto.AttachParams{Session: "s-404"}); err == nil {
		t.Fatal("attach to an unknown session succeeded")
	}
	a, info, err := Attach(p, proto.AttachParams{Session: id, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if info.ID != id || info.Cols != 80 {
		t.Fatalf("attached %+v (attaching must not resize the pane)", info)
	}

	type frame struct {
		typ     proto.FrameType
		payload []byte
	}
	frames := make(chan frame, 1024)
	go func() {
		defer close(frames)
		for {
			typ, payload, err := a.ReadFrame()
			if err != nil {
				return
			}
			frames <- frame{typ, append([]byte(nil), payload...)}
		}
	}()
	first := <-frames
	if first.typ != proto.FrameSnapshot {
		t.Fatalf("first frame %d, want snapshot", first.typ)
	}
	mirror, err := emu.Decode(first.payload)
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	a.WriteFrame(proto.FrameInput, []byte("echo via-attach\r"))
	// A console resizes the pane through its view: here a bare one of
	// its own, showing the session, in a 140×40 window.
	st, v, err := SubscribeView(p, proto.ViewSubscribeParams{Own: true, Bare: true, Session: id, Cols: 140, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	want := v.Lay(140, 40).Area
	call(t, c, proto.MethodViewSize, proto.ViewParams{Client: st.Client, Cols: 140, Rows: 40, Resize: true}, &v)
	a.WriteFrame(proto.FrameInput, []byte("echo after-resize\r"))

	serverScreen := func() string {
		var rr proto.SessionReadResult
		call(t, c, proto.MethodSessionRead, proto.SessionReadParams{ID: id}, &rr)
		return rr.Text
	}
	resized := false
	digestAsked := false
	serverDigest := ""
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("stream ended")
			}
			switch f.typ {
			case proto.FrameOutput:
				mirror.Write(f.payload)
			case proto.FrameResize:
				cols, rows, err := proto.ParseSize(f.payload)
				if err != nil {
					t.Fatal(err)
				}
				mirror.Resize(cols, rows)
				resized = int(cols) == want.W && int(rows) == want.H
			case proto.FrameDigest:
				serverDigest = string(f.payload)
			}
		case <-deadline:
			got, _ := mirror.Screen()
			t.Fatalf("mirror never matched the server (resized=%v)\nmirror:\n%s\nserver:\n%s", resized, got, serverScreen())
		}
		got, _ := mirror.Screen()
		if !digestAsked && resized && strings.Contains(got, "after-resize") && got == serverScreen() {
			// Quiet now: ask for the server's digest at this point of the
			// stream and compare it with the mirror's.
			a.WriteFrame(proto.FrameDigestReq, nil)
			digestAsked = true
		}
		if serverDigest != "" {
			mine, _ := mirror.Digest()
			if mine != serverDigest {
				t.Fatalf("mirror digest %s, server %s", mine, serverDigest)
			}
			break
		}
	}

	a.WriteFrame(proto.FrameDetach, nil)
	for range frames {
	}
	// Detaching leaves the session running.
	if !strings.Contains(serverScreen(), "after-resize") {
		t.Fatal("session gone after detach")
	}
}

func TestResolvePathsKeepsTestsIsolated(t *testing.T) {
	t.Setenv("TERMINATR_HOME", "/h/custom")
	t.Setenv("TERMINATR_SOCKET", "/tmp/x/tm.sock")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1")
	p, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.Socket != "/tmp/x/tm.sock" || p.Lock != "/tmp/x/server.lock" || p.PID != "/tmp/x/server.pid" {
		t.Fatalf("lock and pid must sit next to the socket: %+v", p)
	}
	if p.Log != "/h/custom/logs/server.log" || p.Sessions != "/h/custom/state/sessions.json" {
		t.Fatalf("home files: %+v", p)
	}
	t.Setenv("TERMINATR_SOCKET", "")
	p, _ = ResolvePaths()
	if p.Socket != "/h/custom/run/tm.sock" {
		t.Fatalf("a custom home must not use XDG_RUNTIME_DIR: %s", p.Socket)
	}
}

func TestPrivateDirIsEnforced(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o755)
	if err := ensurePrivateDir(dir); err == nil {
		t.Fatal("accepted a 0755 socket directory")
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(dir, link)
	if err := ensurePrivateDir(link); err == nil {
		t.Fatal("accepted a symlinked socket directory")
	}
}

func TestLogRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	r, err := openRotating(path)
	if err != nil {
		t.Fatal(err)
	}
	r.max = 100
	line := []byte(strings.Repeat("x", 59) + "\n")
	for i := 0; i < 10; i++ {
		r.Write(line)
	}
	r.Close()
	for _, f := range []string{path, path + ".1", path + ".2", path + ".3"} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s missing", f)
		}
	}
	if _, err := os.Stat(path + ".4"); err == nil {
		t.Error("kept more than 3 old logs")
	}
}

func TestSessionEnvStripsInheritedIdentity(t *testing.T) {
	env := sessionEnv([]string{"PATH=/bin", "CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli", "TMUX=x", "CLAUDE_CODE_USE_BEDROCK=1",
		"TERMINATR_SESSION=old", "TERM=screen", "HOME=/h"}, map[string]string{"TERM": "xterm-256color", "TERMINATR_SESSION": "s-1"})
	got := strings.Join(env, " ")
	for _, bad := range []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "TMUX", "TERMINATR_SESSION=old", "TERM=screen"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s leaked: %s", bad, got)
		}
	}
	for _, good := range []string{"PATH=/bin", "HOME=/h", "TERM=xterm-256color", "TERMINATR_SESSION=s-1", "CLAUDE_CODE_USE_BEDROCK=1"} {
		if !strings.Contains(got, good) {
			t.Errorf("%s missing: %s", good, got)
		}
	}
}

func TestWithBinFirst(t *testing.T) {
	got := withBinFirst([]string{"HOME=/h", "PATH=/opt/homebrew/bin:/run/bin:/usr/bin"}, "/run/bin/tm")
	if want := "PATH=/run/bin:/opt/homebrew/bin:/usr/bin"; got[1] != want || got[0] != "HOME=/h" {
		t.Errorf("got %v, want %s", got, want)
	}
	again := withBinFirst(got, "/run/bin/tm")
	if again[1] != got[1] {
		t.Errorf("resume duplicated the entry: %v", again)
	}
	if got := withBinFirst([]string{"HOME=/h"}, "/run/bin/tm"); got[1] != "PATH=/run/bin" {
		t.Errorf("no PATH: %v", got)
	}
}
