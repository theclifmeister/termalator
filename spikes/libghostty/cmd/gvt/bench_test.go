package main

import (
	"bytes"
	"fmt"
	"testing"
)

func seqBytes(n int) []byte {
	var b bytes.Buffer
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d\r\n", i)
	}
	return b.Bytes()
}

// BenchmarkVTWrite measures libghostty parse+apply throughput for
// different PTY read sizes (macOS PTY reads are often tiny).
func BenchmarkVTWrite(b *testing.B) {
	data := seqBytes(200000)
	for _, chunk := range []int{64, 1024, 65536} {
		b.Run(fmt.Sprintf("chunk=%d", chunk), func(b *testing.B) {
			term := newTestTerm(&testing.T{}, 160, 50)
			defer term.Close()
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				for off := 0; off < len(data); off += chunk {
					end := min(off+chunk, len(data))
					term.VTWrite(data[off:end])
				}
			}
		})
	}
}

// BenchmarkFrame measures a full-screen render of a 160x50 screen.
func BenchmarkFrame(b *testing.B) {
	term := newTestTerm(&testing.T{}, 160, 50)
	defer term.Close()
	for i := 0; i < 60; i++ {
		fmt.Fprintf(term, "\x1b[1;3%dmline %d\x1b[0m 漢字 👋🏽 %s\r\n", i%8, i, bytes.Repeat([]byte("x"), 100))
	}
	r, _ := newRenderer()
	r.outCols, r.outRows = 160, 50
	for i := 0; i < b.N; i++ {
		r.full = true
		r.frame(term, false)
	}
}

// BenchmarkSnapshot measures snapshot encode+decode with 10k scrollback rows.
func BenchmarkSnapshot(b *testing.B) {
	term := newTestTerm(&testing.T{}, 160, 50)
	defer term.Close()
	term.VTWrite(seqBytes(10000))
	snap, _ := term.Snapshot()
	b.Logf("snapshot with 10k rows: %d bytes", len(snap))
	for i := 0; i < b.N; i++ {
		s, _ := term.Snapshot()
		t, _ := decodeSnapshot(s)
		t.Close()
	}
}
