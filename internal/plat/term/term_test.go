//go:build unix

package term

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNotATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if IsTerminal(r) || RawMode(r) {
		t.Fatal("a pipe counts as a terminal")
	}
	if _, _, ok := Size(r); ok {
		t.Fatal("a pipe has a size")
	}
	if _, err := MakeRaw(r); err == nil {
		t.Fatal("MakeRaw on a pipe worked")
	}
}

func TestEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	evs := Events(ctx)
	next := func() Event {
		t.Helper()
		select {
		case ev := <-evs:
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		return Event{}
	}
	// Events starts its signal handler asynchronously: send until one
	// arrives.
	got := make(chan Event, 1)
	go func() { got <- next() }()
	for len(got) == 0 {
		unix.Kill(os.Getpid(), unix.SIGWINCH)
		time.Sleep(10 * time.Millisecond)
	}
	if ev := <-got; ev.Kind != Resize {
		t.Fatalf("SIGWINCH gave %+v", ev)
	}
	for len(evs) > 0 {
		<-evs
	}
	unix.Kill(os.Getpid(), unix.SIGHUP)
	if ev := next(); ev.Kind != Detach || ev.Why != "hangup" {
		t.Fatalf("SIGHUP gave %+v", ev)
	}
	cancel()
	for range evs {
	}
}
