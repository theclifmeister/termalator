package proc

import (
	"errors"
	"slices"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processCommandLineInformation is the NtQueryInformationProcess class
// (Windows 8.1 and later) that returns the command line as a
// UNICODE_STRING, without reading the process's memory, so it works
// across architectures (x64 under emulation reading an ARM64 process).
const processCommandLineInformation = 60

// Lookup tells about the process pid.
func Lookup(pid int) (Info, error) {
	if pid <= 0 {
		return Info{}, ErrNoProcess
	}
	ppid, err := parentPID(pid)
	if err != nil {
		return Info{}, err
	}
	in := Info{PID: pid, PPID: ppid}
	fill(&in)
	if !Alive(pid) {
		return Info{}, ErrNoProcess
	}
	return in, nil
}

// List tells about every process (a Toolhelp snapshot, then the details
// of each that the caller may open).
func List() ([]Info, error) {
	ents, err := snapshot()
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(ents))
	for _, e := range ents {
		if e.pid == 0 {
			continue // the System Idle Process
		}
		in := Info{PID: e.pid, PPID: e.ppid}
		fill(&in)
		out = append(out, in)
	}
	return out, nil
}

type entry struct{ pid, ppid int }

func snapshot() ([]entry, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	pe := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var out []entry
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		out = append(out, entry{int(pe.ProcessID), int(pe.ParentProcessID)})
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return out, nil
}

func parentPID(pid int) (int, error) {
	ents, err := snapshot()
	if err != nil {
		return 0, err
	}
	for _, e := range ents {
		if e.pid == pid {
			return e.ppid, nil
		}
	}
	return 0, ErrNoProcess
}

// fill adds what the process's owner lets us read: argv, exe, start
// time. Other users' processes keep only their pids.
func fill(in *Info) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(in.PID))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	var c, x, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &x, &k, &u) == nil {
		in.Started = time.Unix(0, c.Nanoseconds())
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
		in.Exe = windows.UTF16ToString(buf[:n])
	}
	if cl, ok := commandLine(h); ok {
		if argv, err := windows.DecomposeCommandLine(cl); err == nil && len(argv) > 0 {
			in.Argv = argv
		}
	}
}

func commandLine(h windows.Handle) (string, bool) {
	size := uint32(1024)
	for range 4 {
		buf := make([]byte, size)
		var need uint32
		err := windows.NtQueryInformationProcess(h, processCommandLineInformation, unsafe.Pointer(&buf[0]), size, &need)
		switch {
		case err == nil:
			us := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
			return us.String(), true
		case errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH),
			errors.Is(err, windows.STATUS_BUFFER_OVERFLOW),
			errors.Is(err, windows.STATUS_BUFFER_TOO_SMALL):
			size = max(need, size*2)
		default:
			return "", false
		}
	}
	return "", false
}

// descendants returns pid's descendants, deepest last: a process is a
// child of p only if p did not start after it (a dead parent's pid may be
// reused).
func descendants(pid int) []int {
	all, err := List()
	if err != nil {
		return nil
	}
	var root Info
	for _, in := range all {
		if in.PID == pid {
			root = in
		}
	}
	if root.PID != pid {
		return nil
	}
	tree := []Info{root}
	for i := 0; i < len(tree); i++ {
		for _, in := range all {
			if tree[i].ParentOf(in) && in.PID != pid && !slices.ContainsFunc(tree, func(t Info) bool { return t.PID == in.PID }) {
				tree = append(tree, in)
			}
		}
	}
	var out []int
	for _, in := range tree[1:] {
		out = append(out, in.PID)
	}
	return out
}
