package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleWn  = kernel32.NewProc("GetConsoleWindow")
	procGetHandleInfo = kernel32.NewProc("GetHandleInformation")
)

// Alive reports whether a process has pid, whoever owns it.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// Another user's process is there but closed to us; a pid that
		// names no process is an invalid parameter.
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	// A handle can outlive its process: ask whether it has exited.
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// Terminate stops pid and its descendants. Windows has no polite stop for
// a process it didn't start (callers ask over the control socket first),
// so this is Kill.
func Terminate(pid int) error { return Kill(pid) }

// Kill stops pid and every process below it at once (TerminateProcess;
// Windows has no process groups to signal). It reports pid's own failure:
// descendants that are gone or closed to us are skipped. Never this
// process.
func Kill(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("kill pid %d: %w", pid, ErrNoProcess)
	}
	// List the tree before killing: once pid is dead, its children no
	// longer show it as their parent.
	kids := descendants(pid)
	err := terminate(pid)
	for _, k := range kids {
		if k != os.Getpid() {
			terminate(k)
		}
	}
	return err
}

func terminate(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return fmt.Errorf("kill pid %d: %w", pid, ErrNoProcess)
		}
		return err
	}
	defer windows.CloseHandle(h)
	if ev, _ := windows.WaitForSingleObject(h, 0); ev != uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("kill pid %d: %w", pid, ErrNoProcess)
	}
	return windows.TerminateProcess(h, 1)
}

// Exec runs bin with argv and env on this console, waits for it and exits
// with its code: Windows can't replace a process. This process stays
// alive until then and keeps what it holds, in particular a lock that
// flock.Inheritable passed on (a Windows lock belongs to the process, not
// the handle). Every inheritable handle of this process passes to the
// child. The child is in a job that dies with this process, so it never
// outlives the lock. It returns only if the program can't start.
func Exec(bin string, argv, env []string) error {
	if len(argv) == 0 {
		argv = []string{bin}
	}
	cmd := exec.Command(bin, argv[1:]...)
	cmd.Args = argv
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: inheritableHandles()}
	// Ctrl-C and Ctrl-Break reach the child too, on the shared console;
	// this process waits for its verdict.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	if err := cmd.Start(); err != nil {
		signal.Stop(sigs)
		return err
	}
	bindToJob(cmd.Process.Pid)
	err := cmd.Wait()
	if cmd.ProcessState == nil {
		fmt.Fprintln(os.Stderr, "exec:", err)
		os.Exit(1)
	}
	os.Exit(cmd.ProcessState.ExitCode())
	return nil
}

// inheritableHandles lists this process's handles that are marked
// inheritable (Go's own are not), for the child to get beside its
// standard handles. Handle values are multiples of four.
func inheritableHandles() []syscall.Handle {
	var hs []syscall.Handle
	for h := windows.Handle(4); h < 1<<14; h += 4 {
		var flags uint32
		if r, _, _ := procGetHandleInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&flags))); r != 0 && flags&windows.HANDLE_FLAG_INHERIT != 0 {
			hs = append(hs, syscall.Handle(h))
		}
	}
	return hs
}

// bindToJob puts pid in a job object that kills it when this process
// goes, however it goes. Best effort: a job that already holds this
// process may forbid it, in which case the child simply runs.
func bindToJob(pid int) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(h)
	// The job handle stays open (leaked) until this process exits.
	if windows.AssignProcessToJobObject(job, h) != nil {
		windows.CloseHandle(job)
	}
}

// StartDetached starts s without a console window, in a new process
// group, so it outlives this process and its console (never
// DETACHED_PROCESS: every console child of it would flash a window of its
// own). It leaves the job this process runs in when the job allows it
// (CREATE_BREAKAWAY_FROM_JOB): a session's job kills its tree when the
// session closes, and a server auto-started from a session must outlive
// it. It inherits no handles beyond s's standard files, and with no Dir
// it starts in the temp directory, so it holds no directory that someone
// wants to delete or rename. Wait on the process to learn whether it
// exited early, or Release it.
func StartDetached(s Spec) (*os.Process, error) {
	if len(s.Argv) == 0 {
		return nil, errors.New("start: no program")
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer devnull.Close()
	or := func(f *os.File) *os.File {
		if f == nil {
			return devnull
		}
		return f
	}
	start := func(flags uint32) (*os.Process, error) {
		cmd := exec.Command(s.Argv[0], s.Argv[1:]...)
		cmd.Dir, cmd.Env = s.Dir, s.Env
		if cmd.Dir == "" {
			cmd.Dir = os.TempDir()
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = or(s.Stdin), or(s.Stdout), or(s.Stderr)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd.Process, nil
	}
	const base = windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP
	p, err := start(base | windows.CREATE_BREAKAWAY_FROM_JOB)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// A job that doesn't allow breakaway (a CI runner's, say) refuses
		// the flag: start inside it, as this process is.
		p, err = start(base)
	}
	return p, err
}

// Detached reports whether this process has no console window: it was
// started by StartDetached (or by a service), not from a terminal.
func Detached() bool {
	hwnd, _, _ := procGetConsoleWn.Call()
	return hwnd == 0
}

// Detach finishes turning this process into a daemon: stdio on the null
// device, the console released, the working directory the temp
// directory. (A process in its own group already ignores Ctrl-C.)
func Detach() error {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	h := windows.Handle(devnull.Fd())
	for _, id := range []uint32{windows.STD_INPUT_HANDLE, windows.STD_OUTPUT_HANDLE, windows.STD_ERROR_HANDLE} {
		if err := windows.SetStdHandle(id, h); err != nil {
			return err
		}
	}
	// os.Stdin and friends hold the old handles; the null device's file
	// stays open for good, so these never dangle.
	os.Stdin, os.Stdout, os.Stderr = devnull, devnull, devnull
	if !Detached() {
		kernel32.NewProc("FreeConsole").Call()
	}
	return os.Chdir(os.TempDir())
}
