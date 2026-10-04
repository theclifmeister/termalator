package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Protocol is the version of the control API and the attach framing. It
// goes up when a method's meaning or the attach framing changes. 2: the
// server-owned views (view.*), which consoles need. 3: the sidebar's
// tree (view.project, view.expand).
const Protocol = 3

// Kind is what a connection is for.
type Kind string

const (
	KindControl Kind = "control" // NDJSON requests and events
	KindAttach  Kind = "attach"  // binary frames for one pane
	KindHook    Kind = "hook"    // one hook event from `tm hook`
)

// Hello is the first line each side sends.
type Hello struct {
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
	Build    string `json:"build"`          // version.BuildID()
	Kind     Kind   `json:"kind,omitempty"` // client only
	Bin      string `json:"bin,omitempty"`  // server only: its executable, for re-exec
	PID      int    `json:"pid,omitempty"`  // server only
}

// MismatchError says the client can't talk to this server. When ReExec is
// set, the client should re-exec the server's binary (Hello.Bin) with the
// same arguments, so both sides are the same build.
type MismatchError struct {
	Reason string
	ReExec bool
	Bin    string // the server's executable, when ReExec is set
}

func (e *MismatchError) Error() string { return e.Reason }

// Check decides whether a client may proceed after the hello exchange.
//
// Control and hook connections accept an older or equal client protocol:
// methods and fields are only ever added. Attach needs the identical
// build, because the client mirrors the server's emulator from a
// libghostty snapshot whose format is not stable between builds.
func Check(client, server Hello) error {
	if client.Protocol > server.Protocol {
		return &MismatchError{Reason: fmt.Sprintf(
			"tm server speaks protocol %d (%s), this tm speaks %d (%s); run 'tm server restart' (agents are resumed)",
			server.Protocol, server.Version, client.Protocol, client.Version)}
	}
	if client.Kind == KindAttach && client.Build != server.Build {
		return &MismatchError{
			Reason: fmt.Sprintf("tm server is build %s, this tm is %s", server.Build, client.Build),
			ReExec: server.Bin != "",
			Bin:    server.Bin,
		}
	}
	return nil
}

// FrameType tags an attach frame.
type FrameType uint8

const (
	// server → client
	FrameSnapshot FrameType = 1 // libghostty snapshot of the pane's emulator
	FrameOutput   FrameType = 2 // raw PTY output, in order after the snapshot
	FrameResize   FrameType = 3 // u16 cols, u16 rows: the pane was resized at this point in the stream
	FrameDigest   FrameType = 4 // emulator state digest at this point (consistency check)
	FrameState    FrameType = 5 // JSON: agent state, progress, for the status line
	FrameClosed   FrameType = 6 // UTF-8 reason: the session exited or the server is stopping

	// client → server
	FrameInput     FrameType = 10 // bytes for the PTY, already encoded for the pane's modes
	FrameSetSize   FrameType = 11 // u16 cols, u16 rows: the user really resized the window or changed its split panes
	FrameDigestReq FrameType = 12 // ask for a FrameDigest in the stream
	FrameDetach    FrameType = 13
	// FrameColorScheme carries one byte, 1 dark or 2 light: the client's
	// terminal reported its colour scheme. Programs that enabled mode
	// 2031 get a report, and CSI ? 996 n is answered with it.
	FrameColorScheme FrameType = 14
	// FrameClaimSize is u16 cols, u16 rows: the user typed into this pane
	// in a console where its rectangle has that size. The console typed
	// in sizes the pane (docs/SPEC.md §3.3), unless the agent's manifest
	// says screen.resize = "explicit".
	FrameClaimSize FrameType = 15
)

// MaxFrame bounds a frame's payload.
const MaxFrame = 64 << 20

// ErrFrameTooLarge is returned for a payload over MaxFrame.
var ErrFrameTooLarge = errors.New("proto: frame too large")

// WriteFrame writes one frame: type u8, length u32 big-endian, payload.
// It is a single Write: a frame split over two writes could be read and
// acted on between them (a DETACH whose peer has already hung up), and on
// Linux even the empty second write of a payload-less frame then fails
// with EPIPE.
func WriteFrame(w io.Writer, t FrameType, payload []byte) error {
	if len(payload) > MaxFrame {
		return ErrFrameTooLarge
	}
	_, err := w.Write(AppendFrame(make([]byte, 0, 5+len(payload)), t, payload))
	return err
}

// ReadFrame reads one frame. buf is reused when it is large enough.
func ReadFrame(r io.Reader, buf []byte) (FrameType, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFrame {
		return 0, nil, ErrFrameTooLarge
	}
	if cap(buf) < int(n) {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return FrameType(hdr[0]), buf, nil
}

// Size encodes cols and rows for FrameResize, FrameSetSize and
// FrameClaimSize.
func Size(cols, rows uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, cols)
	binary.BigEndian.PutUint16(b[2:], rows)
	return b
}

// ParseSize decodes a size payload.
func ParseSize(b []byte) (cols, rows uint16, err error) {
	if len(b) != 4 {
		return 0, 0, fmt.Errorf("proto: size payload is %d bytes, want 4", len(b))
	}
	return binary.BigEndian.Uint16(b), binary.BigEndian.Uint16(b[2:]), nil
}
