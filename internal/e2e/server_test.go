package e2e

// M1 scenarios: the server lifecycle with real processes. TestSmoke* run
// on every PR; the rest nightly (docs/SPEC.md §16.1).

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/theclifmeister/termalator/internal/proto"
)

const wait = 10 * time.Second

// controllingTTY returns ps's tty column for pid ("?" or "??" for none).
func controllingTTY(t *testing.T, pid int) string {
	t.Helper()
	out, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func staleSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
}

// TestSmokeServerSurvivesClientAndTerminal is M1's "Try it": a user in a
// terminal window starts a session (auto-starting the server); the client
// is killed and the window closed; a new window still finds the session.
func TestSmokeServerSurvivesClientAndTerminal(t *testing.T) {
	env := New(t)

	// Leftovers of a crashed server must not get in the way.
	staleSocket(t, env.Socket)
	os.WriteFile(filepath.Join(filepath.Dir(env.Socket), "server.pid"), []byte("999999\n"), 0o600)

	w := env.Shell(80, 24)
	w.Type(`"$TM" session start -- printer -lines 3; echo client-$((1+1))-done` + "\r")
	screen := w.WaitFor("client-2-done", wait)
	// The id may share a line with the shell's prompt.
	m := regexp.MustCompile(`(?m)\bs-\d+$`).FindString(screen)
	if m == "" {
		t.Fatalf("no session id in the window:\n%s", screen)
	}
	s := &Session{ID: m}
	for _, info := range env.Sessions() {
		if info.ID == s.ID {
			s.PID = info.PID
		}
	}
	env.track(s.PID, "session "+s.ID)

	spid := env.ServerPID()
	if !Alive(spid) {
		t.Fatalf("no server running (pid file says %d)", spid)
	}
	// Full detachment: its own session, no controlling terminal, stdio on
	// /dev/null, a private socket.
	if sid, _ := unix.Getsid(spid); sid != spid {
		t.Errorf("server sid %d, want %d (setsid)", sid, spid)
	}
	if wsid, _ := unix.Getsid(w.PID()); wsid == spid {
		t.Error("server shares the window's session")
	}
	if tty := controllingTTY(t, spid); tty != "?" && tty != "??" {
		t.Errorf("server has controlling tty %q", tty)
	}
	if runtime.GOOS == "linux" {
		for _, fd := range []string{"0", "1", "2"} {
			if target, _ := os.Readlink("/proc/" + strconv.Itoa(spid) + "/fd/" + fd); target != os.DevNull {
				t.Errorf("server fd %s -> %q, want /dev/null", fd, target)
			}
		}
	}
	if fi, err := os.Stat(env.Socket); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode: %v %v", fi, err)
	}

	// Kill the client's whole process group, then close the window, then
	// send the signals a terminal or a careless user might still send.
	w.KillClient()
	w.CloseWindow()
	syscall.Kill(spid, syscall.SIGHUP)
	syscall.Kill(spid, syscall.SIGINT)
	time.Sleep(200 * time.Millisecond)
	env.AssertAlive(s)

	// A new window finds the session and its screen.
	w2 := env.Shell(80, 24)
	w2.Type(`"$TM" session read ` + s.ID + "\r")
	w2.WaitFor("line 3", wait)
	if env.ServerPID() != spid {
		t.Fatalf("the server was replaced: pid %d -> %d", spid, env.ServerPID())
	}

	if out := env.MustCLI("server", "stop"); !strings.Contains(out, "stopped") {
		t.Fatalf("server stop: %s", out)
	}
	if !Poll(3*time.Second, func() bool { return !Alive(spid) && !Alive(s.PID) }) {
		t.Fatal("server or session still running after stop")
	}
	if _, err := os.Stat(env.Socket); !os.IsNotExist(err) {
		t.Fatal("socket left after stop")
	}
	st, _ := os.ReadFile(filepath.Join(env.Home, "state", "sessions.json"))
	if !strings.Contains(string(st), `"shutdown": "clean"`) || !strings.Contains(string(st), s.ID) {
		t.Fatalf("sessions.json after stop:\n%s", st)
	}
}

// TestSmokeSessionScreen: a session's screen as the server sees it, against
// a golden file.
func TestSmokeSessionScreen(t *testing.T) {
	env := New(t)
	s := env.StartSize(40, 8, "printer", "-lines", "3")
	env.WaitFor(s, "ready", wait)
	Golden(t, env.Screen(s), "printer-40x8.txt")
	env.MustCLI("session", "stop", s.ID)
	if r := env.CLI("session", "read", s.ID); r.Code != 1 || !strings.Contains(r.Stderr, "unknown-session") {
		t.Fatalf("read after stop: %+v", r)
	}
}

func TestSmokeServerCommands(t *testing.T) {
	env := New(t)

	if r := env.CLI("server", "status"); r.Code != 1 || !strings.Contains(r.Stdout, "not running") {
		t.Fatalf("status before start: %+v", r)
	}
	if r := env.CLI("server", "stop"); r.Code != 0 || !strings.Contains(r.Stdout, "not running") {
		t.Fatalf("stop while stopped: %+v", r)
	}

	// Started by hand from a shell, it must still detach.
	env.MustCLI("server", "run", "--detached")
	if !Poll(wait, func() bool { return env.CLI("server", "status").Code == 0 }) {
		t.Fatal("hand-started server never answered")
	}
	spid := env.ServerPID()
	if sid, _ := unix.Getsid(spid); sid != spid {
		t.Errorf("hand-started server sid %d, want %d", sid, spid)
	}

	if out := env.MustCLI("server", "start"); !strings.Contains(out, "already running") {
		t.Fatalf("second start: %q", out)
	}
	if r := env.CLI("server", "run"); r.Code != 1 || !strings.Contains(r.Stderr, "already running (pid "+strconv.Itoa(spid)+")") {
		t.Fatalf("second run: %+v", r)
	}
	out := env.MustCLI("server", "status")
	for _, want := range []string{"pid       " + strconv.Itoa(spid), "protocol  " + strconv.Itoa(proto.Protocol), "sessions  0"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if r := env.CLI("session", "stop", "s-99"); r.Code != 0 {
		t.Fatalf("stop is idempotent: %+v", r)
	}
	if r := env.CLI("session", "bogus"); r.Code != 2 {
		t.Fatalf("usage error: %+v", r)
	}
}

// hello performs a hand-made handshake and reports the server's hello and
// whether it then serves the connection.
func hello(t *testing.T, socket string, h proto.Hello) (proto.Hello, bool) {
	t.Helper()
	c, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	b, _ := json.Marshal(h)
	c.Write(append(b, '\n'))
	br := bufio.NewReader(c)
	line, err := br.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var reply proto.Hello
	if err := json.Unmarshal(line, &reply); err != nil {
		t.Fatal(err)
	}
	if h.Kind == proto.KindAttach {
		b, _ = json.Marshal(proto.AttachRequest{Attach: proto.AttachParams{Session: "s-404"}})
	} else {
		b, _ = json.Marshal(proto.Request{ID: 1, Method: proto.MethodPing})
	}
	c.Write(append(b, '\n'))
	_, err = br.ReadBytes('\n')
	return reply, err == nil
}

// TestSmokeVersionHandshake: another build may use control calls of an
// equal or older protocol but never attach; a newer protocol is refused.
func TestSmokeVersionHandshake(t *testing.T) {
	env := New(t)
	env.MustCLI("server", "start")
	var st proto.ServerStatus
	if err := json.Unmarshal([]byte(env.MustCLI("server", "status", "--json")), &st); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		h    proto.Hello
		ok   bool
	}{
		{"control, other build", proto.Hello{Protocol: proto.Protocol, Build: "other", Kind: proto.KindControl}, true},
		{"control, newer protocol", proto.Hello{Protocol: proto.Protocol + 1, Build: st.Build, Kind: proto.KindControl}, false},
		{"attach, same build", proto.Hello{Protocol: proto.Protocol, Build: st.Build, Kind: proto.KindAttach}, true},
		{"attach, other build", proto.Hello{Protocol: proto.Protocol, Build: "other", Kind: proto.KindAttach}, false},
	}
	for _, c := range cases {
		reply, served := hello(t, env.Socket, c.h)
		if served != c.ok {
			t.Errorf("%s: served=%v, want %v", c.name, served, c.ok)
		}
		// The server advertises its pinned copy of env.Bin (§3.6, Upgrade).
		if reply.PID != st.PID || reply.Build != st.Build || filepath.Dir(reply.Bin) != filepath.Join(env.Home, "server-bin") {
			t.Errorf("%s: server hello %+v", c.name, reply)
		}
		if err := proto.Check(c.h, reply); (err == nil) != c.ok {
			t.Errorf("%s: client-side Check = %v", c.name, err)
		}
	}
}

// TestCrashThenAutoStart: SIGKILL the server; the next client finds the
// stale socket, starts a new server, and that server reports the crash.
func TestCrashThenAutoStart(t *testing.T) {
	env := New(t)
	s := env.Start("shell")
	old := env.ServerPID()
	env.KillServer()
	if !Poll(wait, func() bool { return !Alive(s.PID) }) {
		t.Fatal("the session outlived its server")
	}
	if r := env.CLI("server", "status"); r.Code != 1 || !strings.Contains(r.Stdout, "not running") {
		t.Fatalf("status after crash: %+v", r)
	}
	if _, err := os.Stat(env.Socket); !os.IsNotExist(err) {
		t.Fatal("the stale socket was not cleaned up")
	}
	env.MustCLI("session", "list") // auto-starts
	if pid := env.ServerPID(); pid == old || !Alive(pid) {
		t.Fatal("no new server")
	}
	out := env.MustCLI("server", "status")
	if !strings.Contains(out, "previous server crashed") || !strings.Contains(out, s.ID) {
		t.Fatalf("crash not reported:\n%s", out)
	}
}

// TestHungServer: something holds the lock and the socket but never
// answers. Clients must say so promptly instead of hanging.
func TestHungServer(t *testing.T) {
	env := New(t)
	run := filepath.Dir(env.Socket)
	f, err := os.OpenFile(filepath.Join(run, "server.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(run, "server.pid"), []byte("4242\n"), 0o600)
	ln, err := net.Listen("unix", env.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		var conns []net.Conn
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c) // accept, never answer
		}
	}()
	t0 := time.Now()
	r := env.CLI("session", "list")
	if r.Code != 3 || !strings.Contains(r.Stderr, "server unresponsive (pid 4242)") ||
		!strings.Contains(r.Stderr, "tm server stop --force") {
		t.Fatalf("hung server: %+v", r)
	}
	if d := time.Since(t0); d > 5*time.Second {
		t.Fatalf("took %v to give up", d)
	}
}

// TestRestartServer: a clean restart keeps the record of what ran.
func TestRestartServer(t *testing.T) {
	env := New(t)
	s := env.Start("printer")
	old := env.ServerPID()
	env.RestartServer()
	if pid := env.ServerPID(); pid == old || !Alive(pid) {
		t.Fatal("restart did not start a new server")
	}
	if !Poll(3*time.Second, func() bool { return !Alive(s.PID) }) {
		t.Fatal("shell sessions are not kept across a restart in v0.1")
	}
	var st proto.ServerStatus
	json.Unmarshal([]byte(env.MustCLI("server", "status", "--json")), &st)
	if st.PreviousShutdown != "clean" || len(st.Lost) != 1 || st.Lost[0] != s.ID {
		t.Fatalf("status after restart: %+v", st)
	}
}

// longHome moves env to a TERMALATOR_HOME too long for a socket under it,
// without the harness's TERMALATOR_SOCKET override, so the server's run
// directory falls back to /tmp. It returns the home.
func longHome(env *Env) string {
	h := filepath.Join(env.T.TempDir(), strings.Repeat("h", 100), "termalator")
	var vars []string
	for _, kv := range env.Vars {
		if !strings.HasPrefix(kv, "TERMALATOR_SOCKET=") && !strings.HasPrefix(kv, "TERMALATOR_HOME=") {
			vars = append(vars, kv)
		}
	}
	env.Vars = append(vars, "TERMALATOR_HOME="+h)
	env.Home = h
	return h
}

// serverSocket asks the running server for its socket path.
func serverSocket(env *Env) string {
	env.T.Helper()
	for _, l := range strings.Split(env.MustCLI("server", "status"), "\n") {
		if v, ok := strings.CutPrefix(l, "socket"); ok {
			return strings.TrimSpace(v)
		}
	}
	env.T.Fatal("server status names no socket")
	return ""
}

// TestSmokeLongHomesSeparateServers: two TERMALATOR_HOMEs too long for a
// socket under them each get their own fallback run directory under /tmp,
// and so their own server and sessions (docs/SPEC.md §3.2).
func TestSmokeLongHomesSeparateServers(t *testing.T) {
	a, b := New(t), New(t)
	longHome(a)
	longHome(b)
	sa := a.Start("printer", "-lines", "1")
	sb := b.Start("printer", "-lines", "2")
	a.Socket, b.Socket = serverSocket(a), serverSocket(b)
	t.Cleanup(func() { // runs before the harness's own cleanup
		a.CLI("server", "stop")
		b.CLI("server", "stop")
		os.RemoveAll(filepath.Dir(a.Socket))
		os.RemoveAll(filepath.Dir(b.Socket))
	})
	if a.Socket == b.Socket {
		t.Fatalf("both homes use the socket %s", a.Socket)
	}
	for _, s := range []string{a.Socket, b.Socket} {
		if !strings.HasPrefix(s, "/tmp/termalator-") || len(s) > 100 {
			t.Fatalf("fallback socket %s", s)
		}
	}
	if pa, pb := a.ServerPID(), b.ServerPID(); pa == 0 || pa == pb {
		t.Fatalf("server pids %d and %d", pa, pb)
	}
	if la, lb := a.Sessions(), b.Sessions(); len(la) != 1 || len(lb) != 1 || la[0].PID == lb[0].PID {
		t.Fatalf("sessions: %+v and %+v", la, lb)
	}
	a.WaitFor(sa, "line 1", wait)
	b.WaitFor(sb, "line 2", wait)
	// Stopping one server leaves the other running.
	a.MustCLI("server", "stop")
	b.AssertAlive(sb)
}
