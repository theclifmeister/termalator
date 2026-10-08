package proc

import (
	"bytes"
	"encoding/binary"
	"time"

	"golang.org/x/sys/unix"
)

// Lookup tells about the process pid (kern.proc.pid, kern.procargs2).
func Lookup(pid int) (Info, error) {
	if pid <= 0 {
		return Info{}, ErrNoProcess
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if (err != nil || kp.Proc.P_pid != int32(pid)) && !Alive(pid) {
		// The sysctl says EIO for a missing pid.
		return Info{}, ErrNoProcess
	}
	if err != nil {
		return Info{}, err
	}
	if kp.Proc.P_pid != int32(pid) {
		return Info{}, ErrNoProcess
	}
	return info(kp), nil
}

// List tells about every process (kern.proc.all).
func List() ([]Info, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(kps))
	for i := range kps {
		if kps[i].Proc.P_pid > 0 {
			out = append(out, info(&kps[i]))
		}
	}
	return out, nil
}

func info(kp *unix.KinfoProc) Info {
	in := Info{
		PID:     int(kp.Proc.P_pid),
		PPID:    int(kp.Eproc.Ppid),
		Started: time.Unix(kp.Proc.P_starttime.Unix()),
	}
	in.Exe, in.Argv = procArgs(in.PID)
	return in
}

// procArgs reads a process's exec path and argv from kern.procargs2:
// argc, the exec path, padding, then argv as NUL-terminated strings. It
// fails, leaving both empty, for another user's process or a zombie.
func procArgs(pid int) (exe string, argv []string) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return "", nil
	}
	argc := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return "", nil
	}
	exe, b = string(b[:i]), b[i:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	for len(argv) < argc && len(b) > 0 {
		j := bytes.IndexByte(b, 0)
		if j < 0 {
			j = len(b)
		}
		argv = append(argv, string(b[:j]))
		if j == len(b) {
			break
		}
		b = b[j+1:]
	}
	return exe, argv
}
