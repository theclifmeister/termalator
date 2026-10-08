//go:build unix

package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// helperEnv makes the test binary act as a helper process (TestMain).
const helperEnv = "TERMINATR_PROC_HELPER"

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "":
		os.Exit(m.Run())
	case "exec":
		err := Exec("/bin/sh", []string{"sh", "-c", `echo "exec $X"`}, []string{"X=ok"})
		fmt.Println("exec failed:", err)
		os.Exit(1)
	case "detach":
		// Started detached: report, detach, report again to a file
		// (stdout is the null device by then).
		out := os.Getenv("OUT")
		before := Detached()
		err := Detach()
		wd, _ := os.Getwd()
		os.WriteFile(out, fmt.Appendf(nil, "%v %v %s", before, err, wd), 0o600)
		os.Exit(0)
	}
	os.Exit(2)
}

func helper(t *testing.T, kind string, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), append(env, helperEnv+"="+kind)...)
	return cmd
}

func needsInfo(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no Lookup on ", runtime.GOOS)
	}
}

// sleeper starts /bin/sleep and waits until it has exec'd.
func sleeper(t *testing.T) (*exec.Cmd, Info) {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	var in Info
	var err error
	for range 200 {
		// Until the child has exec'd its argv is empty (Linux) or the
		// parent's (macOS).
		if in, err = Lookup(cmd.Process.Pid); err == nil && slices.Equal(in.Argv, []string{"/bin/sleep", "30"}) {
			return cmd, in
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Lookup = %+v, %v", in, err)
	return nil, Info{}
}

func TestLookup(t *testing.T) {
	needsInfo(t)
	cmd, in := sleeper(t)
	if in.PID != cmd.Process.Pid || in.PPID != os.Getpid() {
		t.Fatalf("pid %d ppid %d, want %d %d", in.PID, in.PPID, cmd.Process.Pid, os.Getpid())
	}
	// Linux resolves the path (/bin may link to /usr/bin).
	if filepath.Base(in.Exe) != "sleep" {
		t.Errorf("Exe = %q", in.Exe)
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
	if len(self.Argv) == 0 || self.Argv[0] != os.Args[0] {
		t.Errorf("own argv %q, want %q first", self.Argv, os.Args[0])
	}
}

func TestLookupGone(t *testing.T) {
	needsInfo(t)
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if _, err := Lookup(pid); !errors.Is(err, ErrNoProcess) {
		t.Errorf("Lookup of a reaped pid: %v, want ErrNoProcess", err)
	}
	if Alive(pid) {
		t.Error("Alive of a reaped pid")
	}
	for _, pid := range []int{0, -1} {
		if _, err := Lookup(pid); !errors.Is(err, ErrNoProcess) {
			t.Errorf("Lookup(%d): %v, want ErrNoProcess", pid, err)
		}
	}
}

func TestList(t *testing.T) {
	needsInfo(t)
	cmd, _ := sleeper(t)
	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	found := map[int]Info{}
	for _, in := range all {
		found[in.PID] = in
	}
	if in := found[os.Getpid()]; len(in.Argv) == 0 || in.Argv[0] != os.Args[0] {
		t.Errorf("own entry %+v", in)
	}
	if in := found[cmd.Process.Pid]; !slices.Equal(in.Argv, []string{"/bin/sleep", "30"}) || in.PPID != os.Getpid() {
		t.Errorf("child entry %+v", in)
	}
}

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("this process is not alive")
	}
	if !Alive(1) {
		t.Error("init is not alive (another user's process counts)")
	}
	if Alive(0) || Alive(-1) {
		t.Error("pid 0 or -1 is alive")
	}
}

func TestTerminateAndKill(t *testing.T) {
	for _, c := range []struct {
		stop func(int) error
		sig  syscall.Signal
	}{{Terminate, syscall.SIGTERM}, {Kill, syscall.SIGKILL}} {
		cmd := exec.Command("/bin/sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := c.stop(cmd.Process.Pid); err != nil {
			t.Fatal(err)
		}
		cmd.Wait()
		ws := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !ws.Signaled() || ws.Signal() != c.sig {
			t.Errorf("exit %v, want %v", cmd.ProcessState, c.sig)
		}
		if err := c.stop(cmd.Process.Pid); !errors.Is(err, ErrNoProcess) {
			t.Errorf("stopping a reaped pid: %v, want ErrNoProcess", err)
		}
	}
	// kill(2) signals a group or every process for these: never.
	for _, pid := range []int{0, -1} {
		if err := Kill(pid); !errors.Is(err, ErrNoProcess) {
			t.Errorf("Kill(%d): %v, want ErrNoProcess", pid, err)
		}
	}
}

func TestExec(t *testing.T) {
	out, err := helper(t, "exec").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "exec ok" {
		t.Fatalf("output %q, %v", out, err)
	}
}

func TestStartDetachedAndDetach(t *testing.T) {
	if Detached() {
		t.Skip("the test itself runs as a session leader")
	}
	out := filepath.Join(t.TempDir(), "out")
	cmd := helper(t, "detach", "OUT="+out)
	p, err := StartDetached(Spec{Argv: append([]string{cmd.Path}, cmd.Args[1:]...), Env: cmd.Env, Dir: t.TempDir()})
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
	if got := string(b); got != "true <nil> /" {
		t.Fatalf("helper said %q, want detached, no error, cwd /", got)
	}
}
