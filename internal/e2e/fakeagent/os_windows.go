package main

import (
	"os"
	"os/exec"
)

// sockDir holds the messaging sockets.
var sockDir = os.TempDir()

// shell is the shell that runs hook commands and the Bash tool's: sh on
// PATH (Git for Windows', as Claude Code uses Git Bash), else sh.exe for
// a clear error.
func shell() string {
	if p, err := exec.LookPath("sh"); err == nil {
		return p
	}
	return "sh.exe"
}
