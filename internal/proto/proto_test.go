package proto

import (
	"bytes"
	"errors"
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
