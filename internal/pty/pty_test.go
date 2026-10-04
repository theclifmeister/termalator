package pty

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"
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
	argv, err := ProcArgs(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 2 || argv[0] != "/bin/sleep" || argv[1] != "5" {
		t.Fatalf("argv %q", argv)
	}
}
