package pty

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

// ProcArgs returns a process's argv (kern.procargs2: argc, the exec path,
// padding, then argv as NUL-terminated strings).
func ProcArgs(pid int) ([]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, fmt.Errorf("pty: procargs2 of %d: short", pid)
	}
	argc := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	i := bytes.IndexByte(b, 0) // the exec path
	if i < 0 {
		return nil, fmt.Errorf("pty: procargs2 of %d: no exec path", pid)
	}
	b = b[i:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	var out []string
	for len(out) < argc && len(b) > 0 {
		j := bytes.IndexByte(b, 0)
		if j < 0 {
			j = len(b)
		}
		out = append(out, string(b[:j]))
		if j == len(b) {
			break
		}
		b = b[j+1:]
	}
	return out, nil
}
