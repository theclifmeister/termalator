package session

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/termalator/internal/emu"
	"github.com/theclifmeister/termalator/internal/proto"
)

func start(t *testing.T, argv ...string) *Session {
	t.Helper()
	s, err := Start(Config{
		ID:   "s-test",
		Role: proto.RoleShell,
		Argv: argv,
		Cwd:  t.TempDir(),
		Env:  []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color", "PS1=$ ", "LANG=C.UTF-8"},
		Cols: 80, Rows: 24,
		Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop(time.Second) })
	return s
}

// eventually polls cond until it holds or the deadline passes.
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

func screen(t *testing.T, s *Session) string {
	t.Helper()
	r, err := s.Read(false)
	if err != nil {
		t.Fatal(err)
	}
	return r.Text
}

func TestInputAndRead(t *testing.T) {
	s := start(t, "/bin/sh")
	s.Input([]byte("echo hi-$((40+2))\r"))
	eventually(t, "echo output", func() bool { return strings.Contains(screen(t, s), "hi-42") })
}

// mirror plays an attach client: it rebuilds the pane from the snapshot
// and then applies the ordered stream.
type mirror struct {
	term    *emu.Terminal
	snaps   int
	closed  string
	updates chan struct{}
}

func runMirror(t *testing.T, sub *Subscriber) *mirror {
	m := &mirror{updates: make(chan struct{}, 1)}
	frames := make(chan [][2][]byte)
	go func() {
		defer close(frames)
		for {
			b, ok := sub.Next()
			if !ok {
				return
			}
			var batch [][2][]byte
			r := bytes.NewReader(b)
			for r.Len() > 0 {
				typ, payload, err := proto.ReadFrame(r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				batch = append(batch, [2][]byte{{byte(typ)}, append([]byte(nil), payload...)})
			}
			frames <- batch
		}
	}()
	go func() {
		for batch := range frames {
			for _, f := range batch {
				m.apply(t, proto.FrameType(f[0][0]), f[1])
			}
			select {
			case m.updates <- struct{}{}:
			default:
			}
		}
	}()
	return m
}

func (m *mirror) apply(t *testing.T, typ proto.FrameType, payload []byte) {
	switch typ {
	case proto.FrameSnapshot:
		term, err := emu.Decode(payload)
		if err != nil {
			t.Error(err)
			return
		}
		if m.term != nil {
			m.term.Close()
		}
		m.term = term
		m.snaps++
	case proto.FrameOutput:
		m.term.Write(payload)
	case proto.FrameResize:
		c, r, err := proto.ParseSize(payload)
		if err != nil {
			t.Error(err)
			return
		}
		m.term.Resize(c, r)
	case proto.FrameClosed:
		m.closed = string(payload)
	}
}

func TestAttachMirrorMatchesServer(t *testing.T) {
	s := start(t, "/bin/sh")
	s.Input([]byte("echo before-attach\r"))
	eventually(t, "first output", func() bool { return strings.Contains(screen(t, s), "before-attach") })

	sub, err := s.Attach()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Detach()
	m := runMirror(t, sub)

	s.Input([]byte("for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do echo line-$i; done\r"))
	eventually(t, "loop output", func() bool { return strings.Contains(screen(t, s), "line-30") })
	if err := s.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	s.Input([]byte("echo after-resize\r"))
	eventually(t, "post-resize output", func() bool { return strings.Contains(screen(t, s), "after-resize") })

	eventually(t, "mirror to match the server", func() bool {
		<-m.updates
		got, _ := m.term.Screen()
		return got == screen(t, s)
	})
	if c, r := m.term.Size(); c != 100 || r != 30 {
		t.Fatalf("mirror size %d×%d, want 100×30", c, r)
	}
	if m.snaps != 1 {
		t.Fatalf("mirror got %d snapshots, want 1", m.snaps)
	}
}

func TestSlowClientIsResynced(t *testing.T) {
	old := MaxPending
	MaxPending = 2048
	defer func() { MaxPending = old }()

	s := start(t, "/bin/sh")
	sub, err := s.Attach()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Detach()
	// Nobody drains the subscriber while this runs.
	// The echoed command line must not match: wait for computed output.
	s.Input([]byte("i=0; while [ $i -lt 2000 ]; do echo flood-$i; i=$((i+1)); done; echo done-$((6*7))\r"))
	eventually(t, "flood to finish", func() bool { return strings.Contains(screen(t, s), "done-42") })
	if sub.Resyncs() == 0 {
		t.Fatal("expected at least one resync")
	}
	m := runMirror(t, sub)
	eventually(t, "mirror to match after resync", func() bool {
		<-m.updates
		got, _ := m.term.Screen()
		return got == screen(t, s)
	})
}

func TestStopEscalatesToKill(t *testing.T) {
	s := start(t, "/bin/sh", "-c", "trap '' HUP; echo ready; while :; do sleep 1; done")
	eventually(t, "ready", func() bool { return strings.Contains(screen(t, s), "ready") })
	t0 := time.Now()
	s.Stop(300 * time.Millisecond)
	select {
	case <-s.Done():
	default:
		t.Fatal("session still running after Stop")
	}
	if d := time.Since(t0); d < 300*time.Millisecond {
		t.Fatalf("stopped after %v; SIGHUP should have been ignored", d)
	}
	if st := s.ExitStatus(); !strings.Contains(st, "killed") {
		t.Fatalf("exit status %q, want killed", st)
	}
}

func TestExitClosesSubscribers(t *testing.T) {
	exited := make(chan string, 1)
	s, err := Start(Config{
		ID: "s-exit", Role: proto.RoleShell,
		Argv: []string{"/bin/sh", "-c", "read x; exit 3"},
		Cwd:  "/", Env: []string{"PATH=/usr/bin:/bin"},
		OnExit: func(s *Session) { exited <- s.ExitStatus() },
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.Attach()
	if err != nil {
		t.Fatal(err)
	}
	s.Input([]byte("\r"))
	var last proto.FrameType
	for {
		b, ok := sub.Next()
		if !ok {
			break
		}
		r := bytes.NewReader(b)
		for r.Len() > 0 {
			typ, _, err := proto.ReadFrame(r, nil)
			if err != nil {
				t.Fatal(err)
			}
			last = typ
		}
	}
	if last != proto.FrameClosed {
		t.Fatalf("last frame %d, want FrameClosed", last)
	}
	select {
	case st := <-exited:
		if st != "exit status 3" {
			t.Fatalf("exit status %q", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnExit not called")
	}
	if _, err := s.Read(false); err != ErrExited {
		t.Fatalf("Read after exit: %v", err)
	}
}
