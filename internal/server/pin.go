package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/theclifmeister/terminatr/internal/plat/flock"
	"github.com/theclifmeister/terminatr/internal/plat/fsx"
	"github.com/theclifmeister/terminatr/internal/plat/proc"
	"github.com/theclifmeister/terminatr/internal/service"
)

// pinDir holds the server's own copy of its binary, under the run dir.
const pinDir = service.PinDir

// pinName is that copy's name. It is the same for every build: macOS
// privacy settings (TCC) know a command-line tool by its path, so one
// path keeps one entry, and one answer, across upgrades. launchd starts
// the server from it (service.Config.Program), because TCC keeps the
// path launchd started a process with, whatever that process execs. On
// Windows it is tm.exe.
const pinName = service.PinName + pinExt

// legacyPinDir is where servers before T87 pinned it, under the home.
const legacyPinDir = "server-bin"

// pinBinary keeps the binary a server runs reachable for as long as the
// server runs, and returns the path to use for it (docs/SPEC.md §3.6,
// Upgrade).
//
// Attach clients of another build re-exec the server's binary, and agent
// hooks run it. Both break when an upgrade replaces the file: `tm update`
// renames a new binary over it, and `brew upgrade` deletes the old keg.
// So the server puts a copy of its binary at RunDir/bin/tm and uses that
// path instead. A different binary there is replaced by a rename, so a
// process starting it runs the old file or the new one, never half of
// one, and one still running the old file keeps it. The copy carries the
// same code signature. Only the lock holder calls this, so no other
// server runs from the pin; it also removes everything else in the
// directory (the per-build pins of T87).
func pinBinary(runDir, bin string) (string, error) {
	if bin == "" {
		return "", fmt.Errorf("no binary")
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	dir := filepath.Join(runDir, pinDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, pinName)
	defer removeOtherPins(dir)
	pinCompanions(filepath.Dir(bin), dir)
	if same, err := sameContent(bin, dst); err != nil {
		return "", err
	} else if same {
		return dst, nil
	}
	err := fsx.Replace(dst, 0o700, func(tmp string) error {
		if err := cloneFile(bin, tmp); err != nil {
			os.Remove(tmp)
			return copyFile(bin, tmp)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return dst, nil
}

// EnsurePin puts bin at the pin in runDir when there is none, for a
// launchd job that runs the pin (service.Config.Program) before any
// server has pinned itself. An existing pin is left alone: a running
// server may use it, and the server re-pins from the launch file when it
// starts.
func EnsurePin(runDir, bin string) error {
	if _, err := os.Stat(filepath.Join(runDir, pinDir, pinName)); err == nil {
		return nil
	}
	_, err := pinBinary(runDir, bin)
	return err
}

// pinFor pins the binary for a server running bin: source when it is
// set and exists (launchd runs the pin, and the launch file names the tm
// that started it), else bin. It returns the pin and whether the server
// must exec it: when it runs another file, or the pin now holds another
// build.
func pinFor(runDir, bin, source string, logf func(string, ...any)) (string, bool, error) {
	src := bin
	if source != "" {
		if _, err := os.Stat(source); err == nil {
			src = source
		} else {
			logf("pin %s: %v; pinning %s", source, err, bin)
		}
	}
	before, _ := os.Stat(filepath.Join(runDir, pinDir, pinName))
	pin, err := pinBinary(runDir, src)
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", src, err)
	}
	after, err := os.Stat(pin)
	replaced := before == nil || err != nil || !os.SameFile(before, after)
	return pin, replaced || !fsx.SamePath(bin, pin), nil
}

// pinCompanions copies the files tm needs beside it (companions) from
// src to the pin's dir, when src has them and the pin's differ. It does
// its best: a companion in use stays as it is, and a missing one makes
// tm fall back (to the system's ConPTY on Windows).
func pinCompanions(src, dir string) {
	for _, name := range companions {
		from, to := filepath.Join(src, name), filepath.Join(dir, name)
		if same, err := sameContent(from, to); err != nil || same {
			continue
		}
		fsx.Replace(to, 0o700, func(tmp string) error { return copyFile(from, tmp) })
	}
}

// removeOtherPins removes all but the pin and its companions from dir:
// older per-build pins and temporary files a crash left. A process still
// running one keeps its file until it exits.
func removeOtherPins(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != pinName && !slices.Contains(companions, e.Name()) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// sameContent reports whether the regular file dst holds what src does.
// A missing dst is not the same.
func sameContent(src, dst string) (bool, error) {
	si, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	di, err := os.Stat(dst)
	if err != nil || !di.Mode().IsRegular() {
		return false, nil
	}
	if os.SameFile(si, di) {
		return true, nil
	}
	if si.Size() != di.Size() {
		return false, nil
	}
	a, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer a.Close()
	b, err := os.Open(dst)
	if err != nil {
		return false, nil
	}
	defer b.Close()
	ba, bb := make([]byte, 1<<16), make([]byte, 1<<16)
	for {
		na, ea := io.ReadFull(a, ba)
		nb, _ := io.ReadFull(b, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		if ea == io.EOF || errors.Is(ea, io.ErrUnexpectedEOF) {
			return true, nil
		}
		if ea != nil {
			return false, ea
		}
	}
}

// lockFDEnv hands the server lock across the exec into the pin: it
// names the inherited descriptor (takeLock, inheritedLock).
const lockFDEnv = "TERMINATR_SERVER_LOCK_FD"

// execPinned runs the pin in place of this process, keeping the pid
// (launchd's job, the caller's wait) and the lock, so no other server can
// start in between. run is proc.Exec (Options.Exec). It returns only
// on failure, with the lock still held and closed on exec again.
func execPinned(lock *lockFile, pin string, args []string, run func(string, []string, []string) error) error {
	fd, err := lock.Inheritable()
	if err != nil {
		return err
	}
	env := []string{lockFDEnv + "=" + strconv.Itoa(fd)}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, lockFDEnv+"=") {
			env = append(env, kv)
		}
	}
	err = run(pin, append([]string{pin}, args...), env)
	lock.Uninheritable()
	return err
}

// inheritedLock adopts the lock a server handed across its exec into
// the pin (execPinned), or returns nil. The descriptor must be the lock
// file at path; the flock it holds is this process's own.
func inheritedLock(path string) *lockFile {
	v, ok := os.LookupEnv(lockFDEnv)
	if !ok {
		return nil
	}
	os.Unsetenv(lockFDEnv)
	fd, err := strconv.Atoi(v)
	if err != nil || fd < 3 {
		return nil
	}
	l := flock.Inherited(fd, path)
	if l == nil {
		return nil
	}
	return &lockFile{Lock: l, handedOver: true}
}

// removeLegacyPins removes the home's old server-bin directory, which an
// older server or dashboard may still run from during an upgrade: a file
// some live process runs is kept (and so is the directory), and the next
// start tries again. If the process list can't be read, nothing is
// removed. Call it only once the new pin is in place.
func removeLegacyPins(home string) {
	dir := filepath.Join(home, legacyPinDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	procs, err := proc.List()
	if err != nil {
		return
	}
	for _, e := range entries {
		f := filepath.Join(dir, e.Name())
		if !e.Type().IsRegular() || !runs(procs, f) {
			os.Remove(f)
		}
	}
	os.Remove(dir) // fails, harmlessly, while files remain
}

// runs reports whether some process runs the program at path.
func runs(procs []proc.Info, path string) bool {
	for _, p := range procs {
		if p.Exe == path || len(p.Argv) > 0 && p.Argv[0] == path {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
