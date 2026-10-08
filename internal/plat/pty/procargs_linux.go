package pty

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
)

// ProcArgs returns a process's argv.
func ProcArgs(pid int) ([]string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, err
	}
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		// A kernel thread, a zombie, or a child that hasn't exec'd yet.
		return nil, fmt.Errorf("pty: no argv for pid %d", pid)
	}
	var out []string
	for _, a := range bytes.Split(b, []byte{0}) {
		out = append(out, string(a))
	}
	return out, nil
}

// ParentPID returns a process's parent pid, from /proc/<pid>/stat (the
// field after the parenthesised command name, which may contain spaces).
func ParentPID(pid int) (int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, fmt.Errorf("pty: bad stat for pid %d", pid)
	}
	f := bytes.Fields(b[i+1:])
	if len(f) < 2 {
		return 0, fmt.Errorf("pty: bad stat for pid %d", pid)
	}
	return strconv.Atoi(string(f[1]))
}
