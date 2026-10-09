//go:build windows

package fsx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A file another process holds open without FILE_SHARE_DELETE blocks the
// rename; it goes through once the holder lets go.
func TestRenameRetriesSharingViolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		windows.CloseHandle(h)
	}()
	if err := WriteAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatalf("file reads %q", b)
	}
}

func TestWriteAtomicReadOnlyTargetHandle(t *testing.T) {
	// The sync bug: every WriteAtomic failed with access denied.
	if err := WriteAtomic(filepath.Join(t.TempDir(), "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestJunctionIsLink(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "real")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, dir).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v: %s", err, out)
	}
	if err := CheckPrivate(link); err == nil {
		t.Fatal("a junction passed")
	}
}

func TestOwnedByMe(t *testing.T) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ownedByMe(u.User.Sid); err != nil || !ok {
		t.Fatalf("own SID: %v %v", ok, err)
	}
	other, _ := windows.StringToSid("S-1-5-32-546") // BUILTIN\Guests
	if ok, _ := ownedByMe(other); ok {
		t.Fatal("Guests accepted")
	}
}

// A running exe can't be overwritten but SwapIn renames it aside; the
// running copy goes on, and CleanAside removes it once the process is gone.
func TestSwapInRunningExe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tm.exe")
	self, _ := os.Executable()
	b, _ := os.ReadFile(self)
	os.WriteFile(path, b, 0o755)
	cmd := exec.Command(path, "-test.run=^$", "-test.timeout=1m")
	cmd.Env = append(os.Environ(), "TERMINATR_FSX_SLEEP=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	nb := filepath.Join(dir, ".new")
	os.WriteFile(nb, []byte("new"), 0o600)
	if _, err := SwapIn(nb, path); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("path holds %d bytes", len(got))
	}
	if left := CleanAside(path); left != 1 {
		t.Errorf("CleanAside with the old exe running: %d left, want 1", left)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if left := CleanAside(path); left != 0 {
		t.Errorf("CleanAside after it exited: %d left", left)
	}
}

// A file open without FILE_SHARE_DELETE makes SwapIn fail, and leaves the
// target as it was.
func TestSwapInHeld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tm.exe")
	os.WriteFile(path, []byte("old"), 0o755)
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	nb := filepath.Join(dir, ".new")
	os.WriteFile(nb, []byte("new"), 0o600)
	if _, err := SwapIn(nb, path); err == nil {
		t.Fatal("SwapIn of a held file succeeded")
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Errorf("path holds %q", got)
	}
	if _, err := os.Stat(nb); err != nil {
		t.Errorf("new file gone: %v", err)
	}
}

// TestMain lets the test binary act as a long-running program for
// TestSwapInRunningExe.
func TestMain(m *testing.M) {
	if os.Getenv("TERMINATR_FSX_SLEEP") != "" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
