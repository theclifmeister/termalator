package main

import (
	"os"
	"testing"
	"time"
)

// An exec-form hook runs its command directly with args: no shell, so a
// path with spaces and shell metacharacters needs no quoting.
func TestRunHookCmdExecForm(t *testing.T) {
	h := hookCmd{command: "/bin/echo", args: []string{"a b", "$HOME;x"}, timeout: 5 * time.Second}
	out, code := runHookCmd(h, nil, t.TempDir(), os.Environ(), false)
	if code != 0 || out != "a b $HOME;x\n" {
		t.Fatalf("exec form = %q, %d", out, code)
	}
}
