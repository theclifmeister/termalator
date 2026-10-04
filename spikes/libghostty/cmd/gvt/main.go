// Command gvt is the Termalator libghostty-vt spike: a detached server that
// owns a PTY + libghostty-vt emulator, and a client that attaches over a
// unix socket, restores a snapshot and then streams.
//
//	gvt new    -s SOCK [-cols N -rows N] -- cmd args   spawn server, then attach
//	gvt server -s SOCK [-cols N -rows N] -- cmd args   run server in foreground
//	gvt attach -s SOCK                                 attach (Ctrl+\ detaches)
//	gvt dump   -s SOCK [-all]                          print the server screen
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"go.mitchellh.com/libghostty"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gvt new|server|attach|dump -s SOCK ...")
		os.Exit(2)
	}
	sub := os.Args[1]
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	sock := fs.String("s", "/tmp/gvt.sock", "unix socket path")
	cols := fs.Uint("cols", 0, "initial columns (default: current terminal)")
	rows := fs.Uint("rows", 0, "initial rows (default: current terminal)")
	statsPath := fs.String("stats", "", "write client stats JSON here on exit")
	logPath := fs.String("log", "", "client log file")
	all := fs.Bool("all", false, "dump: include scrollback")
	vt := fs.Bool("vt", false, "dump: full VT state dump instead of plain text")
	fs.Parse(os.Args[2:])
	argv := fs.Args()

	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			log.SetOutput(f)
		}
	} else if sub != "server" {
		log.SetOutput(nopWriter{})
	}
	log.SetFlags(log.Lmicroseconds)

	var err error
	switch sub {
	case "server":
		err = runServer(*sock, uint16(*cols), uint16(*rows), argv)
	case "new":
		c, r := termSize()
		if *cols == 0 {
			*cols = uint(c)
		}
		if *rows == 0 {
			*rows = uint(r)
		}
		if len(argv) == 0 {
			argv = []string{"claude"}
		}
		if err = spawnServer(*sock, uint16(*cols), uint16(*rows), argv); err == nil {
			err = runAttach(*sock, *statsPath)
		}
	case "attach":
		err = runAttach(*sock, *statsPath)
	case "dump":
		err = runDump(*sock, *all, *vt)
	default:
		err = fmt.Errorf("unknown subcommand %q", sub)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gvt:", err)
		os.Exit(1)
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// runDump attaches as a passive client, decodes the snapshot and prints the
// server's screen. Used by the harness to compare against what the outer
// terminal actually shows.
func runDump(sock string, all, vt bool) error {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return err
	}
	defer conn.Close()
	typ, payload, err := readMsg(conn)
	if err != nil || typ != msgSnapshot {
		return fmt.Errorf("expected snapshot: %v", err)
	}
	t, err := decodeSnapshot(payload)
	if err != nil {
		return err
	}
	defer t.Close()
	if vt {
		fmt.Print(stateDump(t))
		return nil
	}
	fmt.Print(plainText(t, all))
	return nil
}

func decodeSnapshot(b []byte) (*libghostty.Terminal, error) {
	dec, err := libghostty.NewSnapshotDecoderBytesCopy(b)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.Decode()
}

// plainText returns the viewport (or, with all, the whole screen including
// scrollback) as plain text, one line per row.
func plainText(t *libghostty.Terminal, all bool) string {
	if all {
		f, err := libghostty.NewFormatter(t, libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain))
		if err != nil {
			return ""
		}
		defer f.Close()
		s, _ := f.FormatString()
		return s
	}
	// The viewport must come from the render state: slicing the last N
	// formatter lines is wrong when the bottom rows are blank (trimmed).
	rs, err := libghostty.NewRenderState()
	if err != nil {
		return ""
	}
	defer rs.Close()
	rs.Update(t)
	ri, _ := libghostty.NewRenderStateRowIterator()
	defer ri.Close()
	rc, _ := libghostty.NewRenderStateRowCells()
	defer rc.Close()
	rs.RowIterator(ri)
	var lines []string
	for ri.Next() {
		b, _ := ri.AppendText(nil, rc)
		lines = append(lines, string(b))
	}
	return strings.Join(lines, "\n")
}
