package pty

import (
	"bytes"
	"io"
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
