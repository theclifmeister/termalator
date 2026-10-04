package pty

import "golang.org/x/sys/unix"

// ParentPID returns the parent of a process.
func ParentPID(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	return int(kp.Eproc.Ppid), nil
}
