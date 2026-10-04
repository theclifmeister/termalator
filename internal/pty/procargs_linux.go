package pty

import (
	"bytes"
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
	var out []string
	for _, a := range bytes.Split(b, []byte{0}) {
		out = append(out, string(a))
	}
	return out, nil
}
