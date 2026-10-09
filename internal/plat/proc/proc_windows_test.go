package proc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// helperEnv makes the test binary act as a helper process (TestMain).
const helperEnv = "TERMINATR_PROC_HELPER"

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "":
		os.Exit(m.Run())
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "spawn":
		// Parent of a grandchild: print its pid, then sleep.
		c := exec.Command(os.Args[0])
		c.Env = append(os.Environ(), helperEnv+"=sleep")
		if err := c.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Println(c.Process.Pid)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "exec":
		err := Exec(os.Args[0], []string{"helper", "-x"}, append(os.Environ(), helperEnv+"=exit7"))
		fmt.Println("exec failed:", err)
		os.Exit(1)
	case "exit7":
		fmt.Println("exec ok", os.Getenv("X"), len(os.Args))
		os.Exit(7)
	case "exechandle":
		// Exec passes an inheritable handle on.
		f, err := os.CreateTemp("", "h")
		if err != nil {
			os.Exit(2)
		}
		defer os.Remove(f.Name())
		windows.SetHandleInformation(windows.Handle(f.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)
		fmt.Println(Exec(os.Args[0], []string{"helper"}, append(os.Environ(), helperEnv+"=gethandle", "H="+strconv.Itoa(int(f.Fd())))))
		os.Exit(1)
	case "gethandle":
		h, _ := strconv.Atoi(os.Getenv("H"))
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(windows.Handle(h), &info); err != nil {
			fmt.Println("handle:", err)
			os.Exit(3)
		}
		os.Exit(0)
	case "startdetached":
		// Wait to be put in a job, then start a detached sleeper.
		bufio.NewReader(os.Stdin).ReadString('\n')
		p, err := StartDetached(Spec{Argv: []string{os.Args[0]}, Env: append(os.Environ(), helperEnv+"=sleep")})
		if err != nil {
			fmt.Println("start:", err)
			os.Exit(2)
		}
		fmt.Println(p.Pid)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "hold":
		// Open F without FILE_SHARE_DELETE, say so, sleep.
		p, _ := windows.UTF16PtrFromString(os.Getenv("F"))
		if _, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0); err != nil {
			fmt.Println(err)
			os.Exit(2)
		}
		fmt.Println("held")
		time.Sleep(time.Minute)
		os.Exit(0)
	case "detach":
		out := os.Getenv("OUT")
		before := Detached()
		err := Detach()
		wd, _ := os.Getwd()
		os.WriteFile(out, fmt.Appendf(nil, "%v %v %s", before, err, wd), 0o600)
		os.Exit(0)
	}
	os.Exit(2)
}

func helper(kind string, env ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), append(env, helperEnv+"="+kind)...)
	return cmd
}

// sleeper starts a helper that sleeps.
func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := helper("sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func TestLookupAndList(t *testing.T) {
	cmd := sleeper(t)
	pid := cmd.Process.Pid
	in, err := Lookup(pid)
	if err != nil {
		t.Fatal(err)
	}
	if in.PID != pid || in.PPID != os.Getpid() {
		t.Fatalf("pid %d ppid %d, want %d %d", in.PID, in.PPID, pid, os.Getpid())
	}
	if len(in.Argv) == 0 || !strings.EqualFold(in.Argv[0], os.Args[0]) {
		t.Errorf("Argv = %q, want %q first", in.Argv, os.Args[0])
	}
	if !strings.EqualFold(in.Exe, os.Args[0]) {
		t.Errorf("Exe = %q, want %q", in.Exe, os.Args[0])
	}
	if d := time.Since(in.Started); d < -2*time.Second || d > time.Minute {
		t.Errorf("Started %v ago", d)
	}
	self, err := Lookup(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !self.ParentOf(in) {
		t.Errorf("this process (started %v) is not the parent of %+v", self.Started, in)
	}
	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(all, func(x Info) bool { return x.PID == pid })
	if i < 0 || all[i].PPID != os.Getpid() || !slices.Equal(all[i].Argv, in.Argv) {
		t.Errorf("List entry %v of %d: %+v", i, pid, all)
	}
}

func TestLookupGone(t *testing.T) {
	cmd := helper("exit7")
	cmd.Run()
	pid := cmd.Process.Pid
	if _, err := Lookup(pid); !errors.Is(err, ErrNoProcess) {
		t.Errorf("Lookup of a finished pid: %v, want ErrNoProcess", err)
	}
	if Alive(pid) {
		t.Error("Alive of a finished pid")
	}
	for _, pid := range []int{0, -1} {
		if _, err := Lookup(pid); !errors.Is(err, ErrNoProcess) {
			t.Errorf("Lookup(%d): %v, want ErrNoProcess", pid, err)
		}
	}
}

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("this process is not alive")
	}
	if !Alive(4) {
		t.Error("System is not alive (another user's process counts)")
	}
	if Alive(0) || Alive(-1) {
		t.Error("pid 0 or -1 is alive")
	}
}

func TestKillTree(t *testing.T) {
	for _, stop := range []func(int) error{Terminate, Kill} {
		cmd := helper("spawn")
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(out).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		grand, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			t.Fatal(err)
		}
		if !Alive(grand) {
			t.Fatalf("grandchild %d is not alive", grand)
		}
		if err := stop(cmd.Process.Pid); err != nil {
			t.Fatal(err)
		}
		cmd.Wait()
		for range 100 {
			if !Alive(grand) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if Alive(grand) {
			t.Errorf("grandchild %d survived its parent", grand)
			Kill(grand)
		}
		if err := stop(cmd.Process.Pid); !errors.Is(err, ErrNoProcess) {
			t.Errorf("stopping a finished pid: %v, want ErrNoProcess", err)
		}
	}
	for _, pid := range []int{0, -1} {
		if err := Kill(pid); !errors.Is(err, ErrNoProcess) {
			t.Errorf("Kill(%d): %v, want ErrNoProcess", pid, err)
		}
	}
}

func TestExec(t *testing.T) {
	out, err := helper("exec", "X=ok").CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("exit: %v, want code 7 (output %q)", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "exec ok ok 2" {
		t.Fatalf("output %q", got)
	}
}

func TestExecPassesHandles(t *testing.T) {
	if out, err := helper("exechandle").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestExecFails(t *testing.T) {
	if err := Exec(filepath.Join(t.TempDir(), "missing.exe"), nil, nil); err == nil {
		t.Fatal("Exec of a missing program succeeded")
	}
}

func TestStartDetachedAndDetach(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	cmd := helper("detach", "OUT="+out)
	p, err := StartDetached(Spec{Argv: append([]string{cmd.Path}, cmd.Args[1:]...), Env: cmd.Env})
	if err != nil {
		t.Fatal(err)
	}
	st, err := p.Wait()
	if err != nil || !st.Success() {
		t.Fatalf("helper: %v, %v", st, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "true <nil> " + os.TempDir()
	if got := string(b); !strings.EqualFold(strings.TrimSuffix(got, `\`), strings.TrimSuffix(want, `\`)) {
		t.Fatalf("helper said %q, want %q (detached, no error, cwd the temp dir)", got, want)
	}
}

// jobWith makes a kill-on-close job, with breakaway allowed or not, and
// puts the started helper cmd in it.
func jobWith(t *testing.T, cmd *exec.Cmd, breakaway bool) windows.Handle {
	t.Helper()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if breakaway {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		t.Skipf("can't put the helper in a job (this process's job forbids it): %v", err)
	}
	return job
}

// startedInJob runs a helper in a kill-on-close job that calls
// StartDetached, closes the job, and returns the detached child's pid.
func startedInJob(t *testing.T, breakaway bool) int {
	t.Helper()
	cmd := helper("startdetached")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	job := jobWith(t, cmd, breakaway)
	io.WriteString(in, "go\n")
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("helper said %q", line)
	}
	t.Cleanup(func() { Kill(pid) })
	windows.CloseHandle(job)
	cmd.Wait()
	return pid
}

func TestStartDetachedBreaksAwayFromJob(t *testing.T) {
	pid := startedInJob(t, true)
	time.Sleep(500 * time.Millisecond)
	if !Alive(pid) {
		t.Error("the detached process died with the job it was started from")
	}
}

func TestStartDetachedInJobWithoutBreakaway(t *testing.T) {
	// The job forbids breakaway: the start must still work (inside it).
	pid := startedInJob(t, false)
	for range 100 {
		if !Alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the process started in a no-breakaway job outlived it")
}

func TestHolders(t *testing.T) {
	file := filepath.Join(t.TempDir(), "held.exe")
	os.WriteFile(file, []byte("x"), 0o600)
	cmd := helper("hold", "F="+file)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil {
		t.Fatalf("helper: %q %v", line, err)
	}
	hs, err := Holders(file, filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(hs, func(h Holder) bool { return h.PID == cmd.Process.Pid }) {
		t.Fatalf("Holders = %v, want pid %d", hs, cmd.Process.Pid)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if hs, err := Holders(file); err != nil || len(hs) != 0 {
		t.Errorf("after the holder exited: %v, %v", hs, err)
	}
	if hs, err := Holders(); err != nil || hs != nil {
		t.Errorf("no paths: %v, %v", hs, err)
	}
}
