package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
)

func TestIsNoViewClient(t *testing.T) {
	no := proto.Errorf(proto.ErrBadParams, "no view client %q", "c-1")
	if !isNoViewClient(fmt.Errorf("view: %w", no)) {
		t.Error("the server forgetting the client is not recognised")
	}
	for _, err := range []error{nil, ErrViewDown, proto.Errorf(proto.ErrBadParams, "no such session")} {
		if isNoViewClient(err) {
			t.Errorf("%v taken for a forgotten client", err)
		}
	}
}

// TestWaitRejoin: a call that the server refused waits for the console's
// new id, and gives up when the console is closed or none comes.
func TestWaitRejoin(t *testing.T) {
	vc := &ViewConn{client: "c-1", up: true, watch: map[chan struct{}]bool{}}
	go func() {
		time.Sleep(50 * time.Millisecond)
		vc.mu.Lock()
		vc.up = false
		vc.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		vc.mu.Lock()
		vc.client, vc.up = "c-1", true // same id on a new server: not yet
		vc.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		vc.mu.Lock()
		vc.client = "c-2"
		vc.mu.Unlock()
	}()
	if got := vc.waitRejoin("c-1", 5*time.Second); got != "c-2" {
		t.Fatalf("waitRejoin = %q, want c-2", got)
	}
	if got := vc.waitRejoin("c-2", 100*time.Millisecond); got != "" {
		t.Fatalf("waitRejoin with no rejoin = %q", got)
	}
	vc.mu.Lock()
	vc.closed = true
	vc.mu.Unlock()
	if got := vc.waitRejoin("c-2", 5*time.Second); got != "" {
		t.Fatalf("waitRejoin on a closed connection = %q", got)
	}
}
