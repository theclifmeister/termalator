package main

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Wire protocol: every message is [type u8][len u32 BE][payload].
// The server sends one msgSnapshot first, then an ordered stream. Anything
// that changes emulator state (output, resize) travels in that one stream so
// the client's mirror applies it at exactly the same point as the server.
const (
	// server -> client
	msgSnapshot = 1 // libghostty snapshot bytes
	msgOutput   = 2 // raw PTY output bytes
	msgResize   = 3 // u16 cols, u16 rows: server terminal was resized here
	msgDigest   = 4 // server state digest at this point in the stream
	msgExit     = 5 // child exited; payload is the exit status text

	// client -> server
	msgInput     = 10 // bytes to write to the PTY
	msgSetSize   = 11 // u16 cols, u16 rows: client wants this size
	msgDigestReq = 12 // ask the server to put a digest into the stream
)

const maxMsg = 64 << 20

func writeMsg(w io.Writer, typ byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readMsg(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxMsg {
		return 0, nil, fmt.Errorf("message too large: %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return hdr[0], buf, nil
}

func sizePayload(cols, rows uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, cols)
	binary.BigEndian.PutUint16(b[2:], rows)
	return b
}

func parseSize(b []byte) (uint16, uint16) {
	if len(b) < 4 {
		return 0, 0
	}
	return binary.BigEndian.Uint16(b), binary.BigEndian.Uint16(b[2:])
}
