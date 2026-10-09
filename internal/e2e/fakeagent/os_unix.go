//go:build unix

package main

// sockDir holds the messaging sockets: macOS temp dirs are too long for
// a socket path.
const sockDir = "/tmp"

// shell is the shell that runs hook commands and the Bash tool's.
func shell() string { return "/bin/sh" }
