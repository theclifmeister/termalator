//go:build !unix

package session

// Not yet ported: sessions don't start without a pty (internal/plat/pty).

func gone(pid int) bool { return false }
