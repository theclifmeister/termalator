package pty

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestStartAndResize(t *testing.T) {
	cmd, f, err := Start([]string{"/bin/sh", "-c", "stty size; echo done"}, "/", []string{"PATH=/usr/bin:/bin"}, 77, 13)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out bytes.Buffer
	io.Copy(&out, f) // ends with EIO once the child exits
	cmd.Wait()
	if !strings.Contains(out.String(), "13 77") || !strings.Contains(out.String(), "done") {
		t.Fatalf("output %q", out.String())
	}
}

func TestProcArgs(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	// Start returns before the child has exec'd; until then its argv
	// is empty (Linux) or the parent's.
	var argv []string
	var err error
	for i := 0; i < 100; i++ {
		if argv, err = ProcArgs(cmd.Process.Pid); err == nil && len(argv) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 2 || argv[0] != "/bin/sleep" || argv[1] != "5" {
		t.Fatalf("argv %q", argv)
	}
}

func TestParentPID(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	got, err := ParentPID(cmd.Process.Pid)
	if err != nil || got != os.Getpid() {
		t.Fatalf("ParentPID = %d, %v; want %d", got, err, os.Getpid())
	}
}
