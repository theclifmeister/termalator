package proto

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	server := Hello{Protocol: 2, Version: "v0.2.0", Build: "b2", Bin: "/usr/local/bin/tm"}
	var mm *MismatchError

	if err := Check(Hello{Protocol: 1, Kind: KindControl, Build: "old"}, server); err != nil {
		t.Fatalf("an older control client must be accepted: %v", err)
	}
	if err := Check(Hello{Protocol: 3, Kind: KindControl}, server); !errors.As(err, &mm) || mm.ReExec {
		t.Fatalf("a newer client must be refused without re-exec, got %v", err)
	}
	// The refusal carries the server's hello (for tm server stop) and
	// names the command that works.
	if mm.Server.Protocol != 2 || !strings.Contains(mm.Error(), "run 'tm server restart'") {
		t.Fatalf("mismatch: %+v", mm)
	}
	if err := Check(Hello{Protocol: 2, Kind: KindAttach, Build: "b1"}, server); !errors.As(err, &mm) || !mm.ReExec {
		t.Fatalf("attach with another build must be refused with re-exec, got %v", err)
	}
	if err := Check(Hello{Protocol: 2, Kind: KindAttach, Build: "b2"}, server); err != nil {
		t.Fatalf("attach with the same build must pass: %v", err)
	}
}

func TestFrames(t *testing.T) {
	var b bytes.Buffer
	if err := WriteFrame(&b, FrameOutput, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&b, FrameResize, Size(120, 40)); err != nil {
		t.Fatal(err)
	}
	typ, p, err := ReadFrame(&b, nil)
	if err != nil || typ != FrameOutput || string(p) != "hello" {
		t.Fatalf("frame 1 = %v %q %v", typ, p, err)
	}
	typ, p, err = ReadFrame(&b, p)
	if err != nil || typ != FrameResize {
		t.Fatalf("frame 2 = %v %v", typ, err)
	}
	if c, r, err := ParseSize(p); err != nil || c != 120 || r != 40 {
		t.Fatalf("size = %d×%d %v", c, r, err)
	}

	big := []byte{byte(FrameOutput), 0xff, 0xff, 0xff, 0xff}
	if _, _, err := ReadFrame(bytes.NewReader(big), nil); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized frame: %v", err)
	}
}

func TestAppendFrameMatchesWriteFrame(t *testing.T) {
	var b bytes.Buffer
	WriteFrame(&b, FrameOutput, []byte("abc"))
	WriteFrame(&b, FrameDetach, nil)
	got := AppendFrame(AppendFrame(nil, FrameOutput, []byte("abc")), FrameDetach, nil)
	if !bytes.Equal(got, b.Bytes()) {
		t.Fatalf("AppendFrame %x, WriteFrame %x", got, b.Bytes())
	}
}

// writeCounter counts Write calls.
type writeCounter struct{ n int }

func (w *writeCounter) Write(p []byte) (int, error) { w.n++; return len(p), nil }

// TestWriteFrameOneWrite: a frame is one write, so the peer can never act
// on a frame (DETACH, then hang up) before its writer has finished it.
func TestWriteFrameOneWrite(t *testing.T) {
	for _, payload := range [][]byte{nil, []byte("abc")} {
		var w writeCounter
		if err := WriteFrame(&w, FrameDetach, payload); err != nil {
			t.Fatal(err)
		}
		if w.n != 1 {
			t.Fatalf("WriteFrame with %d payload bytes made %d writes, want 1", len(payload), w.n)
		}
	}
}
