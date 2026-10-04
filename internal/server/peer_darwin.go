package server

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerCred returns the uid and pid of the process at the other end of a
// unix socket connection.
func peerCred(c *net.UnixConn) (uid int, pid int, err error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var serr error
	err = raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if serr != nil {
			return
		}
		uid = int(cred.Uid)
		pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if err == nil {
		err = serr
	}
	return uid, pid, err
}
