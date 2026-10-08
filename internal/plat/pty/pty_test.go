package pty

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

var testEnv = []string{"PATH=/usr/bin:/bin"}

func TestStart(t *testing.T) {
	c, err := Start([]string{"/bin/sh", "-c", "stty size; echo done; exit 3"}, "/", testEnv, 77, 13)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var out bytes.Buffer
	io.Copy(&out, c) // ends with EIO once the child exits
	err = c.Wait()
	if !strings.Contains(out.String(), "13 77") || !strings.Contains(out.String(), "done") {
		t.Fatalf("output %q", out.String())
	}
	if err == nil || err.Error() != "exit status 3" {
		t.Fatalf("Wait = %v, want exit status 3", err)
	}
	if _, err := Start(nil, "/", testEnv, 80, 24); err == nil {
		t.Fatal("Start(nil) succeeded")
	}
}

// waitFor reads c until its output contains s.
func waitFor(t *testing.T, c Console, s string) {
	t.Helper()
	var out []byte
	buf := make([]byte, 1024)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	for !strings.Contains(string(out), s) {
		n, err := c.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			t.Fatalf("waiting for %q: %v (output %q)", s, err, out)
		}
	}
}

func TestResizeAndForeground(t *testing.T) {
	c, err := Start([]string{"/bin/sh", "-c", "echo ready; read x; stty size; sleep 5"}, "/", testEnv, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Stop(0); c.Wait(); c.Close() }()
	waitFor(t, c, "ready")
	if pg, err := c.Foreground(); err != nil || pg != c.PID() {
		t.Fatalf("Foreground = %d, %v; want the shell %d", pg, err, c.PID())
	}
	if err := c.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("\n"))
	waitFor(t, c, "30 100")
}

func TestStop(t *testing.T) {
	// A hangup ends a plain shell.
	c, err := Start([]string{"/bin/sh", "-c", "echo ready; sleep 30"}, "/", testEnv, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, c, "ready")
	go c.Wait()
	go io.Copy(io.Discard, c)
	if err := c.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop = %v, want a clean hangup", err)
	}
	c.Close()

	// One that ignores it is killed after grace.
	c, err = Start([]string{"/bin/sh", "-c", "trap '' HUP; echo ready; sleep 30"}, "/", testEnv, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, c, "ready")
	waited := make(chan error, 1)
	go func() { waited <- c.Wait() }()
	go io.Copy(io.Discard, c)
	start := time.Now()
	if err := c.Stop(200 * time.Millisecond); err == nil {
		t.Fatal("Stop = nil, want the kill reported")
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("killed after %v, before grace", d)
	}
	select {
	case err := <-waited:
		if err == nil || !strings.Contains(err.Error(), "killed") {
			t.Fatalf("Wait = %v, want killed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still running after SIGKILL")
	}
	c.Close()
}
