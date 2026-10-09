//go:build windows

package flock

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

func TestMain(m *testing.M) {
	if h := os.Getenv("FLOCK_TEST_HANDLE"); h != "" {
		n, _ := strconv.Atoi(h)
		if Inherited(n, os.Getenv("FLOCK_TEST_PATH")) == nil {
			os.Stdout.WriteString("nil\n")
			os.Exit(1)
		}
		os.Stdout.WriteString("ok\n")
		os.Stdin.Read(make([]byte, 1)) // hold the lock until the parent closes stdin
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLockDoesNotBlockContents(t *testing.T) {
	p := lockPath(t)
	os.WriteFile(p, []byte("pid 42"), 0o600)
	l, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	if b, err := os.ReadFile(p); err != nil || string(b) != "pid 42" {
		t.Fatalf("ReadFile while locked = %q, %v", b, err)
	}
}

// TestInheritedAcrossSpawn: the child adopts the handle (Inherited checks
// that it is the file at path and that the lock is held).
func TestInheritedAcrossSpawn(t *testing.T) {
	p := lockPath(t)
	a, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := a.Inheritable()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "FLOCK_TEST_HANDLE="+strconv.Itoa(h), "FLOCK_TEST_PATH="+p)
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(h)}}
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		a.Uninheritable()
		t.Fatal(err)
	}
	a.Uninheritable()
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "ok\r\n" && line != "ok\n" {
		t.Fatalf("child adopted the lock: %q", line)
	}
	// Windows byte-range locks belong to the process that took them, not
	// to the handle, so the lock stays with the parent: it holds while the
	// child runs and ends with the parent's Unlock, whatever the child does.
	if held, err := Held(p); err != nil || !held {
		t.Fatalf("Held with child running = %v, %v; want true", held, err)
	}
	in.Close()
	cmd.Wait()
	if held, err := Held(p); err != nil || !held {
		t.Fatalf("Held after child exit = %v, %v; want true", held, err)
	}
	a.Unlock()
	if held, err := Held(p); err != nil || held {
		t.Fatalf("Held after parent Unlock = %v, %v; want false", held, err)
	}
}
