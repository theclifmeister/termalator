// Package plat is terminatr's platform layer: every syscall, x/sys,
// creack/pty and runtime.GOOS use lives in a package under it, one per
// area (internal/plat/flock, ...), each with a portable API in x.go and
// per-OS files (x_unix.go, x_windows.go, x_other.go). Callers never branch
// on the OS. boundary_test.go enforces the rule for the rest of the module.
package plat
