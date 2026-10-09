//go:build windows

package term

import (
	"context"
	"os"
	"testing"
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

func TestEventsEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	evs := Events(ctx)
	cancel()
	for range evs {
	}
}
