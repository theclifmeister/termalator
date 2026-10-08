//go:build unix

package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// sockDir is a directory short enough for sockets (t.TempDir is too long
// on macOS).
func sockDir(t *testing.T) string {
	dir, err := os.MkdirTemp("/tmp", "tmipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestServerAddr(t *testing.T) {
	if a, err := ServerAddr("/run/x", ""); err != nil || a != "/run/x/tm.sock" {
		t.Fatalf("ServerAddr = %q, %v", a, err)
	}
	if a, err := ServerAddr("/run/x", "/o/s.sock"); err != nil || a != "/o/s.sock" {
		t.Fatalf("override: %q, %v", a, err)
	}
	long := "/" + strings.Repeat("d", MaxPath)
	if _, err := ServerAddr(long, ""); err == nil {
		t.Fatal("a run dir over the budget passed")
	}
	if _, err := ServerAddr("/run/x", long); err == nil {
		t.Fatal("an override over the budget passed")
	}
	if !Fits("/run/x", ServerName) || Fits(long, ServerName) {
		t.Fatal("Fits disagrees with ServerAddr")
	}
	if a := SessionAddr("/run/s/1", "mod.sock"); a != "/run/s/1/mod.sock" {
		t.Fatalf("SessionAddr = %q", a)
	}
}

func TestListenDialPeer(t *testing.T) {
	a := SessionAddr(sockDir(t), "x.sock")
	ln, err := Listen(a)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if st, err := os.Stat(string(a)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v, %v", st.Mode(), err)
	}
	type accepted struct {
		peer Peer
		err  error
	}
	got := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- accepted{err: err}
			return
		}
		defer c.Close()
		p, err := PeerOf(c)
		got <- accepted{p, err}
	}()
	c, err := Dial(ctx(t), a)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := <-got
	if r.err != nil {
		t.Fatalf("PeerOf: %v", r.err)
	}
	if r.peer.PID != os.Getpid() || !r.peer.SameUser {
		t.Fatalf("peer %+v, want pid %d, same user", r.peer, os.Getpid())
	}
}

func TestPeerOfNotASocket(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, err := PeerOf(a); err == nil {
		t.Fatal("PeerOf a pipe succeeded")
	}
}

// TestAbsent: no socket and a socket left by a closed listener both mean
// nothing listens; closing leaves the file.
func TestAbsent(t *testing.T) {
	a := SessionAddr(sockDir(t), "x.sock")
	_, err := Dial(ctx(t), a)
	if !IsAbsent(err) || !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("no socket: %v", err)
	}
	ln, err := Listen(a)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if _, err := os.Stat(string(a)); err != nil {
		t.Fatalf("Close removed the socket: %v", err)
	}
	_, err = Dial(ctx(t), a)
	if !IsAbsent(err) || !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("stale socket: %v", err)
	}
	if IsAbsent(nil) || IsAbsent(os.ErrPermission) || IsAbsent(context.DeadlineExceeded) {
		t.Fatal("IsAbsent of an unrelated error")
	}
	if _, err := Listen(a); err == nil {
		t.Fatal("Listen over an existing socket succeeded")
	}
	if _, err := Listen(SessionAddr(filepath.Join(sockDir(t), "missing"), "x.sock")); err == nil {
		t.Fatal("Listen in a missing dir succeeded")
	}
}
