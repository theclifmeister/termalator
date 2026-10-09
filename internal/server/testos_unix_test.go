//go:build !windows

package server

import "testing"

// testExe is a program file's extension.
const testExe = ""

// chmodApplies: a directory without write permission refuses new files.
const chmodApplies = true

// testSh is the POSIX shell sessions run in tests.
func testSh(t testing.TB) string { return "/bin/sh" }
