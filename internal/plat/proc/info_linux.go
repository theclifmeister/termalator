package proc

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// clockTicks is USER_HZ, the unit of a process's start time in
// /proc/<pid>/stat: 100 on every Linux Go runs on (sysconf(_SC_CLK_TCK)
// needs cgo).
const clockTicks = 100

// Lookup tells about the process pid (/proc/<pid>/stat, cmdline, exe).
func Lookup(pid int) (Info, error) {
	if pid <= 0 {
		return Info{}, ErrNoProcess
	}
	dir := "/proc/" + strconv.Itoa(pid)
	b, err := os.ReadFile(dir + "/stat")
	if errors.Is(err, fs.ErrNotExist) {
		return Info{}, ErrNoProcess
	}
	if err != nil {
		return Info{}, err
	}
	in, err := parseStat(b)
	if err != nil {
		return Info{}, fmt.Errorf("pid %d: %w", pid, err)
	}
	in.PID = pid
	if b, err := os.ReadFile(dir + "/cmdline"); err == nil {
		// Empty for a kernel thread, a zombie, or a child that hasn't
		// exec'd yet.
		if b = bytes.TrimRight(b, "\x00"); len(b) > 0 {
			for _, a := range bytes.Split(b, []byte{0}) {
				in.Argv = append(in.Argv, string(a))
			}
		}
	}
	in.Exe, _ = os.Readlink(dir + "/exe")
	return in, nil
}

// List tells about every process in /proc.
func List() ([]Info, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		if in, err := Lookup(pid); err == nil {
			out = append(out, in)
		}
	}
	return out, nil
}

// parseStat reads the parent pid and the start time from a stat line.
// The fields that follow the parenthesised command name (which may hold
// spaces and parentheses) start with the state; the parent pid is the
// next, the start time (ticks since boot) the 20th.
func parseStat(b []byte) (Info, error) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return Info{}, errors.New("bad stat")
	}
	f := bytes.Fields(b[i+1:])
	if len(f) < 20 {
		return Info{}, errors.New("bad stat")
	}
	ppid, err := strconv.Atoi(string(f[1]))
	if err != nil {
		return Info{}, errors.New("bad stat")
	}
	in := Info{PPID: ppid}
	if ticks, err := strconv.ParseInt(string(f[19]), 10, 64); err == nil {
		if boot := bootTime(); !boot.IsZero() {
			in.Started = boot.Add(time.Duration(ticks) * time.Second / clockTicks)
		}
	}
	return in, nil
}

// bootTime is when the system booted (btime in /proc/stat); zero if
// unknown.
var bootTime = sync.OnceValue(func() time.Time {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "btime "); ok {
			if s, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(s, 0)
			}
		}
	}
	return time.Time{}
})
