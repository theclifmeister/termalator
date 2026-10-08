package server

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestPinBinary: every build pins to the same path; a new build replaces
// the file by a rename, so the original being replaced, and a process
// holding the old pin open, both keep what they had; per-build pins of
// older servers go.
func TestPinBinary(t *testing.T) {
	run := t.TempDir()
	dir := filepath.Join(run, pinDir)
	os.MkdirAll(dir, 0o700)
	old := filepath.Join(dir, "tm-v0.11.3+abc+111")
	os.WriteFile(old, []byte("T87 pin"), 0o700)
	os.WriteFile(filepath.Join(dir, ".tm-1.tmp"), nil, 0o700)

	bin := filepath.Join(t.TempDir(), "tm")
	os.WriteFile(bin, []byte("build one"), 0o755)
	link := filepath.Join(t.TempDir(), "tm")
	os.Symlink(bin, link)
	p1, err := pinBinary(run, link)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != filepath.Join(dir, pinName) {
		t.Fatalf("pinned at %s", p1)
	}
	if b, _ := os.ReadFile(p1); string(b) != "build one" {
		t.Fatalf("pin reads %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("other pins kept: %v", ents)
	}
	// An upgrade renames a new binary over the old one.
	next := bin + ".new"
	os.WriteFile(next, []byte("build two"), 0o755)
	if err := os.Rename(next, bin); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p1); string(b) != "build one" {
		t.Fatalf("pinned copy reads %q after the upgrade", b)
	}
	// The same build again leaves the file alone.
	same := filepath.Join(t.TempDir(), "tm")
	os.WriteFile(same, []byte("build one"), 0o755)
	before, _ := os.Stat(p1)
	if p, err := pinBinary(run, same); err != nil || p != p1 {
		t.Fatalf("repin: %s %v", p, err)
	}
	if after, _ := os.Stat(p1); !os.SameFile(before, after) {
		t.Fatal("same build: the pin was replaced")
	}
	// A new build swaps it at the same path; an open old pin still reads
	// the old build.
	f, err := os.Open(p1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p2, err := pinBinary(run, bin)
	if err != nil || p2 != p1 {
		t.Fatalf("second pin: %s %v", p2, err)
	}
	if b, _ := os.ReadFile(p2); string(b) != "build two" {
		t.Fatalf("second pin reads %q", b)
	}
	if b, _ := io.ReadAll(f); string(b) != "build one" {
		t.Fatalf("the running old pin reads %q", b)
	}
	if fi, _ := os.Stat(p2); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("pin not executable: %v", fi.Mode())
	}
	if _, err := pinBinary(run, filepath.Join(run, "missing")); err == nil {
		t.Fatal("pinning a missing binary: no error")
	}
}

// TestPinRunsAndHandsOverLock: the server runs a copy of itself from the
// pin, and the lock goes with it: the descriptor is open across the exec
// and the exec'd server adopts it rather than taking it again.
func TestPinRunsAndHandsOverLock(t *testing.T) {
	t.Setenv(lockFDEnv, "")
	os.Unsetenv(lockFDEnv)
	run := t.TempDir()
	p := Paths{Lock: filepath.Join(run, "tm.lock"), PID: filepath.Join(run, "tm.pid")}
	lk, err := takeLock(p)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Unlock()
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	pin, err := pinBinary(run, self)
	if err != nil {
		t.Fatal(err)
	}
	var argv, env []string
	err = execPinned(lk, pin, []string{"server", "run"}, func(bin string, a, e []string) error {
		if bin != pin {
			t.Errorf("exec %s", bin)
		}
		argv, env = a, e
		// The exec'd image would see the descriptor: it must stay open
		// across exec.
		flags, err := unix.FcntlInt(lk.Fd(), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC != 0 {
			t.Errorf("lock closes on exec: %d %v", flags, err)
		}
		// Run the exec'd server's half in a child holding the descriptor.
		cmd := exec.Command(self, "-test.run=^TestPinHelperAdopt$")
		cmd.Env = append(e, "TM_PIN_HELPER=adopt", "TM_PIN_LOCK="+p.Lock)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("adopting child: %v\n%s", err, out)
		}
		return syscall.ENOEXEC
	})
	if !errors.Is(err, syscall.ENOEXEC) {
		t.Fatalf("execPinned: %v", err)
	}
	if len(argv) != 3 || argv[0] != pin || argv[1] != "server" {
		t.Fatalf("argv %q", argv)
	}
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, lockFDEnv+"=") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s set %d times", lockFDEnv, n)
	}
	// A failed exec leaves the lock closed on exec again.
	if flags, _ := unix.FcntlInt(lk.Fd(), unix.F_GETFD, 0); flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("lock still open across exec after a failed one")
	}
	// Another process still can't take it.
	if _, err := tryLockElsewhere(t, self, p.Lock); err == nil {
		t.Fatal("lock free after the hand-over failed")
	}
}

// tryLockElsewhere tries the lock from another process.
func tryLockElsewhere(t *testing.T, self, lock string) ([]byte, error) {
	cmd := exec.Command(self, "-test.run=^TestPinHelperAdopt$")
	cmd.Env = append(os.Environ(), "TM_PIN_HELPER=try", "TM_PIN_LOCK="+lock)
	return cmd.CombinedOutput()
}

// TestPinHelperAdopt is the exec'd server of TestPinRunsAndHandsOverLock:
// it adopts the lock it inherited, which a fresh tryLock couldn't take.
func TestPinHelperAdopt(t *testing.T) {
	lock := os.Getenv("TM_PIN_LOCK")
	switch os.Getenv("TM_PIN_HELPER") {
	case "adopt":
		if _, err := tryLock(lock); !errors.Is(err, ErrLocked) {
			t.Fatalf("tryLock in the child: %v, want held", err)
		}
		lk := inheritedLock(lock)
		if lk == nil || !lk.handedOver {
			t.Fatal("inherited lock not adopted")
		}
		if os.Getenv(lockFDEnv) != "" {
			t.Fatal("hand-over variable left for sessions")
		}
		if flags, _ := unix.FcntlInt(lk.Fd(), unix.F_GETFD, 0); flags&unix.FD_CLOEXEC == 0 {
			t.Fatal("adopted lock open across exec")
		}
	case "try":
		if _, err := tryLock(lock); err != nil {
			t.Fatal(err)
		}
	}
}

// TestInheritedLockRefusesOtherFiles: a descriptor that isn't the lock
// file isn't adopted.
func TestInheritedLockRefusesOtherFiles(t *testing.T) {
	dir := t.TempDir()
	other, _ := os.Create(filepath.Join(dir, "other"))
	defer other.Close()
	t.Setenv(lockFDEnv, strconv.Itoa(int(other.Fd())))
	if lk := inheritedLock(filepath.Join(dir, "tm.lock")); lk != nil {
		t.Fatal("adopted a descriptor of another file")
	}
	t.Setenv(lockFDEnv, "x")
	if lk := inheritedLock(filepath.Join(dir, "tm.lock")); lk != nil {
		t.Fatal("adopted a malformed descriptor")
	}
}

// TestRemoveLegacyPins: the old server-bin directory goes, except a file a
// live process runs.
func TestRemoveLegacyPins(t *testing.T) {
	if _, err := processCommands(); err != nil {
		t.Skip("cannot list processes: ", err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, legacyPinDir)
	os.MkdirAll(dir, 0o700)
	idle := filepath.Join(dir, "tm-old")
	os.WriteFile(idle, []byte("x"), 0o700)
	removeLegacyPins(home)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("legacy dir kept: %v", err)
	}

	os.MkdirAll(dir, 0o700)
	os.WriteFile(idle, []byte("x"), 0o700)
	live := filepath.Join(dir, "tm-live")
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	if err := copyFile(self, live); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(live, "-test.run=^TestPinHelperSleep$")
	cmd.Env = append(os.Environ(), "TM_PIN_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Skip("cannot run copied test binary: ", err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	time.Sleep(200 * time.Millisecond)
	removeLegacyPins(home)
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("running binary removed: %v", err)
	}
	if _, err := os.Stat(idle); !os.IsNotExist(err) {
		t.Fatalf("idle pin kept: %v", err)
	}
}

// TestPinHelperSleep is the process TestRemoveLegacyPins keeps running.
func TestPinHelperSleep(t *testing.T) {
	if os.Getenv("TM_PIN_HELPER") == "1" {
		time.Sleep(time.Minute)
	}
}

// TestPinFor: launchd runs the pin and the launch file names the tm that
// started it (T129). The server execs only when it runs another file or
// the pin now holds another build, and a gone source leaves the pin.
func TestPinFor(t *testing.T) {
	run := t.TempDir()
	pin := filepath.Join(run, pinDir, pinName)
	keg := func(v, content string) string {
		p := filepath.Join(t.TempDir(), v, "tm")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o755)
		return p
	}
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, f) }
	check := func(name, bin, source, content string, wantExec bool) {
		t.Helper()
		got, again, err := pinFor(run, bin, source, logf)
		if err != nil || got != pin || again != wantExec {
			t.Fatalf("%s: %s exec=%v %v", name, got, again, err)
		}
		if b, _ := os.ReadFile(pin); string(b) != content {
			t.Fatalf("%s: pin reads %q", name, b)
		}
	}
	one := keg("1.0", "build one")
	// An older tm's start: launchd ran the versioned binary.
	check("from the keg", one, "", "build one", true)
	// launchd runs the pin, which holds the build that started it.
	check("from the pin", pin, one, "build one", false)
	// An upgrade: the launch file names the new build.
	two := keg("2.0", "build two")
	check("upgraded", pin, two, "build two", true)
	// Homebrew removed the keg the launch file names: keep the pin.
	os.Remove(two)
	check("keg gone", pin, two, "build two", false)
	if len(logged) != 1 {
		t.Fatalf("logged %q", logged)
	}
	// Through a symlink, as Homebrew's bin/tm.
	link := filepath.Join(t.TempDir(), "tm")
	os.Symlink(one, link)
	check("through a link", pin, link, "build one", true)
}
