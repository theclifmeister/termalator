package pty

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// console is a process on a pseudo console (ConPTY) in a Job Object of
// its own, which stands in for the Unix process group: everything the
// process starts is in the job, and Stop and Close end the whole tree.
type console struct {
	api     *conptyAPI
	in, out *os.File // our ends of the pipes; Go's poller owns them
	job     windows.Handle
	proc    *os.Process
	pid     int
	exited  chan struct{} // closed when Wait returns

	pcMu     sync.Mutex
	hpc      windows.Handle // 0 once closed
	pcClosed chan struct{}  // closed once ClosePseudoConsole returned

	filt  filter
	rest  []byte // filtered output Read has yet to hand out
	rerr  error  // what ended the output, returned once rest is out
	close sync.Once
}

// Start runs argv in dir on a new pseudo console of the given size,
// in a new Job Object. argv[0] is looked up in this process's PATH, as
// os/exec does; a nil env is this process's environment.
//
// The pipes are overlapped and owned by Go's poller: Close interrupts a
// pending Read, and read deadlines work. ConPTY's own artifacts are
// handled here: the exe path it sets as the title until the program
// sets one is dropped, and its request for focus reports is answered
// with "focused" (see filter).
func Start(argv []string, dir string, env []string, cols, rows uint16) (Console, error) {
	return start(conpty(), argv, dir, env, cols, rows)
}

func start(api *conptyAPI, argv []string, dir string, env []string, cols, rows uint16) (c *console, err error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("pty: empty command")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("pty: start %s: %w", argv[0], err)
	}
	if env == nil {
		env = os.Environ()
	}
	block, err := envBlock(env)
	if err != nil {
		return nil, fmt.Errorf("pty: start %s: %w", argv[0], err)
	}
	var undo []func()
	defer func() {
		if err != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
		}
	}()

	in, inTheirs, err := pipe(false)
	if err != nil {
		return nil, fmt.Errorf("pty: %w", err)
	}
	undo = append(undo, func() { in.Close() })
	out, outTheirs, err := pipe(true)
	if err != nil {
		windows.CloseHandle(inTheirs)
		return nil, fmt.Errorf("pty: %w", err)
	}
	undo = append(undo, func() { out.Close() })
	hpc, err := api.create(cols, rows, inTheirs, outTheirs)
	// The console has its own handles to the pipes now.
	windows.CloseHandle(inTheirs)
	windows.CloseHandle(outTheirs)
	if err != nil {
		return nil, fmt.Errorf("pty: CreatePseudoConsole: %w", err)
	}
	undo = append(undo, func() { api.close(hpc) })

	job, err := newJob()
	if err != nil {
		return nil, fmt.Errorf("pty: job object: %w", err)
	}
	undo = append(undo, func() { windows.CloseHandle(job) }) // kills what is in it

	h, pid, err := spawn(path, argv, dir, block, hpc, job)
	if err != nil {
		return nil, fmt.Errorf("pty: start %s: %w", argv[0], err)
	}
	// Our handle keeps the pid from being reused until FindProcess has
	// opened its own.
	proc, err := os.FindProcess(pid)
	windows.CloseHandle(h)
	if err != nil {
		return nil, fmt.Errorf("pty: start %s: %w", argv[0], err)
	}
	// Without this conhost stays up after the last process on the console
	// has gone, and the output never ends; with it the output ends when
	// they have gone, as a PTY master's does.
	api.release(hpc)

	c = &console{api: api, in: in, out: out, job: job, proc: proc, pid: pid,
		exited: make(chan struct{}), hpc: hpc, pcClosed: make(chan struct{})}
	c.filt = newFilter(path, func() { c.in.Write(focusIn) })
	return c, nil
}

func (c *console) PID() int { return c.pid }

// Holds reports whether pid is in the console's job: every process the
// session started, unless it broke away on purpose.
func (c *console) Holds(pid int) bool {
	pids, err := jobPIDs(c.job)
	return err == nil && slices.Contains(pids, pid)
}

func (c *console) Read(p []byte) (int, error) {
	for len(c.rest) == 0 {
		if c.rerr != nil {
			return 0, c.rerr
		}
		n, err := c.out.Read(p)
		c.rest = c.filt.feed(c.rest[:0], p[:n])
		switch {
		case err == nil:
		case errors.Is(err, os.ErrDeadlineExceeded):
			if len(c.rest) == 0 {
				return 0, err
			}
		default:
			c.rest = c.filt.flush(c.rest)
			c.rerr = err
		}
	}
	n := copy(p, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

func (c *console) Write(p []byte) (int, error) { return c.in.Write(p) }

func (c *console) SetReadDeadline(t time.Time) error { return c.out.SetReadDeadline(t) }

// Resize resizes the pseudo console; ConPTY repaints the screen at the
// new size (the session has resized its emulator first).
func (c *console) Resize(cols, rows uint16) error {
	c.pcMu.Lock()
	defer c.pcMu.Unlock()
	if c.hpc == 0 {
		return fmt.Errorf("pty: resize: console closed")
	}
	if err := c.api.resize(c.hpc, cols, rows); err != nil {
		return fmt.Errorf("pty: resize: %w", err)
	}
	return nil
}

// Foreground is the shell's current command: the newest process in the
// job that the process Start ran started itself, or that process when it
// started none. Windows has no foreground process group; a shell's
// command is its child, and what the command starts in turn is the
// command's.
func (c *console) Foreground() (int, error) {
	pids, err := jobPIDs(c.job)
	if err != nil {
		return 0, fmt.Errorf("pty: foreground: %w", err)
	}
	parents, err := parentPIDs()
	if err != nil {
		return 0, fmt.Errorf("pty: foreground: %w", err)
	}
	fg, newest := c.pid, int64(0)
	for _, pid := range pids {
		if pid == c.pid || parents[pid] != c.pid {
			continue
		}
		if t := created(pid); t > newest {
			fg, newest = pid, t
		}
	}
	return fg, nil
}

func (c *console) Wait() error {
	st, err := c.proc.Wait()
	close(c.exited)
	if err != nil {
		return err
	}
	if !st.Success() {
		return &exec.ExitError{ProcessState: st}
	}
	return nil
}

// Stop closes the pseudo console, which sends every process on it
// CTRL_CLOSE_EVENT (Windows' hangup), then terminates the job if the
// process is still running after grace. What else is left in the job
// ends with Close.
func (c *console) Stop(grace time.Duration) error {
	go c.closeConsole()
	select {
	case <-c.exited:
		return nil
	case <-time.After(grace):
	}
	windows.TerminateJobObject(c.job, 1)
	return fmt.Errorf("still running %v after closing its console; terminated its job", grace)
}

// Close closes the pipes, which interrupts a pending Read, and the
// pseudo console, and terminates whatever is still running in the job.
func (c *console) Close() error {
	c.close.Do(func() {
		c.out.Close()
		c.in.Close()
		c.closeConsole()
		windows.CloseHandle(c.job) // the job kills what is left in it
	})
	return nil
}

// closeConsole closes the pseudo console once. ClosePseudoConsole can
// block while conhost flushes its output and its processes handle
// CTRL_CLOSE_EVENT; other callers wait for it.
func (c *console) closeConsole() {
	c.pcMu.Lock()
	hpc := c.hpc
	c.hpc = 0
	c.pcMu.Unlock()
	if hpc == 0 {
		<-c.pcClosed
		return
	}
	c.api.close(hpc)
	close(c.pcClosed)
}

// spawn starts path suspended on the pseudo console, puts it in job and
// lets it run, so nothing it starts escapes the job. It returns a handle
// to the process, which the caller closes.
func spawn(path string, argv []string, dir string, env []uint16, hpc, job windows.Handle) (windows.Handle, int, error) {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, 0, err
	}
	defer attrs.Delete()
	// The attribute's value is the HPCON itself, not a pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		return 0, 0, err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// No std handles of ours: without this a child of a process whose
	// own std handles are redirected (the server's are) would write
	// there instead of to the pseudo console.
	si.Flags = windows.STARTF_USESTDHANDLES
	app, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return 0, 0, err
	}
	var cwd *uint16
	if dir != "" {
		if cwd, err = windows.UTF16PtrFromString(dir); err != nil {
			return 0, 0, err
		}
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	if err := windows.CreateProcess(app, cmdline, nil, nil, false, flags, &env[0], cwd, &si.StartupInfo, &pi); err != nil {
		return 0, 0, err
	}
	defer windows.CloseHandle(pi.Thread)
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(pi.Process)
		return 0, 0, fmt.Errorf("assign to job: %w", err)
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(pi.Process)
		return 0, 0, fmt.Errorf("resume: %w", err)
	}
	return pi.Process, int(pi.ProcessId), nil
}

// newJob makes a Job Object whose processes die when its last handle
// closes, as a session's do when its PTY master closes. A process may
// still leave it on purpose (CREATE_BREAKAWAY_FROM_JOB), as a detached
// one on Unix leaves the session.
func newJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// jobPIDs lists the processes in job.
func jobPIDs(job windows.Handle) ([]int, error) {
	type idList struct {
		assigned, listed uint32
		ids              [1]uintptr
	}
	for n := 64; ; n *= 4 {
		buf := make([]uintptr, 1+n) // two uint32s, then n ids
		l := (*idList)(unsafe.Pointer(&buf[0]))
		err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf))*uint32(unsafe.Sizeof(buf[0])), nil)
		if errors.Is(err, windows.ERROR_MORE_DATA) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ids := unsafe.Slice(&l.ids[0], l.listed)
		pids := make([]int, len(ids))
		for i, id := range ids {
			pids[i] = int(id)
		}
		return pids, nil
	}
}

// parentPIDs maps every process to its parent.
func parentPIDs() (map[int]int, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	m := map[int]int{}
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		m[int(e.ProcessID)] = int(e.ParentProcessID)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return m, nil
}

// created is when pid started (100 ns ticks), 0 if it can't be told.
func created(pid int) int64 {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return 0
	}
	return c.Nanoseconds()
}

var pipeSeq struct {
	sync.Mutex
	n int
}

// pipe makes a named pipe whose server end, ours, is overlapped (so Go's
// poller owns it) and whose client end, the pseudo console's, is not.
// With inbound we read and the console writes. Only this user may open
// it, and only one end can: the console's is opened right away.
func pipe(inbound bool) (ours *os.File, theirs windows.Handle, err error) {
	pipeSeq.Lock()
	pipeSeq.n++
	n := pipeSeq.n
	pipeSeq.Unlock()
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\pipe\terminatr-conpty-%d-%d-%d`, os.Getpid(), n, time.Now().UnixNano()))
	if err != nil {
		return nil, 0, err
	}
	sa, err := ownerOnly()
	if err != nil {
		return nil, 0, err
	}
	mode, access := uint32(windows.PIPE_ACCESS_OUTBOUND), uint32(windows.GENERIC_READ)
	if inbound {
		mode, access = windows.PIPE_ACCESS_INBOUND, windows.GENERIC_WRITE
	}
	h, err := windows.CreateNamedPipe(name, mode|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, 64<<10, 64<<10, 0, sa)
	if err != nil {
		return nil, 0, fmt.Errorf("pipe: %w", err)
	}
	theirs, err = windows.CreateFile(name, access, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		windows.CloseHandle(h)
		return nil, 0, fmt.Errorf("pipe: %w", err)
	}
	return os.NewFile(uintptr(h), "conpty"), theirs, nil
}

// ownerOnly is a security descriptor that grants this user alone.
func ownerOnly() (*windows.SecurityAttributes, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + u.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, nil
}

// envBlock is env as CreateProcess wants it: NUL-terminated UTF-16
// strings and a final NUL. Like os/exec, it keeps the last of names that
// differ only in case, and adds SYSTEMROOT, which much of Windows needs.
func envBlock(env []string) ([]uint16, error) {
	idx := map[string]int{}
	var kept []string
	for _, kv := range env {
		if strings.IndexByte(kv, 0) >= 0 {
			return nil, fmt.Errorf("environment variable contains NUL")
		}
		k := kv
		if i := strings.IndexByte(kv[1:], '='); i >= 0 { // names like =C: start with '='
			k = kv[:i+1]
		}
		k = strings.ToUpper(k)
		if i, ok := idx[k]; ok {
			kept[i] = kv
			continue
		}
		idx[k] = len(kept)
		kept = append(kept, kv)
	}
	if _, ok := idx["SYSTEMROOT"]; !ok {
		if v, ok := os.LookupEnv("SYSTEMROOT"); ok {
			kept = append(kept, "SYSTEMROOT="+v)
		}
	}
	var b []uint16
	for _, kv := range kept {
		u, err := windows.UTF16FromString(kv) // NUL-terminated
		if err != nil {
			return nil, err
		}
		b = append(b, u...)
	}
	if len(b) == 0 {
		b = append(b, 0)
	}
	return append(b, 0), nil
}
