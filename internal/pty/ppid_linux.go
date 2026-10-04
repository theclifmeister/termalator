package pty

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ParentPID returns the parent of a process.
func ParentPID(pid int) (int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	// "pid (comm) state ppid …"; comm may contain spaces and parens.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	f := strings.Fields(s[i+1:])
	if i < 0 || len(f) < 2 {
		return 0, fmt.Errorf("pty: malformed /proc/%d/stat", pid)
	}
	return strconv.Atoi(f[1])
}
