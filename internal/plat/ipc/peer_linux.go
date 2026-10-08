package ipc

import "golang.org/x/sys/unix"

// peerCred returns the uid and pid of the process at the other end of
// socket fd.
func peerCred(fd int) (uid, pid int, err error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	return int(cred.Uid), int(cred.Pid), nil
}
