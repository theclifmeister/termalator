package ipc

import "golang.org/x/sys/unix"

// peerCred returns the uid and pid of the process at the other end of
// socket fd.
func peerCred(fd int) (uid, pid int, err error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	pid, err = unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	return int(cred.Uid), pid, err
}
