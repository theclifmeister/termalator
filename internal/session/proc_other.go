//go:build !unix

package session

// Not yet ported: sessions don't start without a pty (internal/pty).

func hangup(pid int)    {}
func kill(pid int)      {}
func gone(pid int) bool { return false }
