package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A run's binaries live in a tm-e2e-bin* temp dir with an owner file
// naming the test process. Every tm server and session of the run
// executes from there, so a dir whose owner is gone marks what a dead run
// (a test timeout, a killed go test) left behind: Env cleanup never ran
// for it.
const (
	binPrefix = "tm-e2e-bin"
	ownerFile = "owner.pid"
)

var (
	failedMu sync.Mutex
	failed   []string // failed scenarios with their artifacts, for TestMain
)

// runDirs are the run dirs this run made. A server runs from its pin in
// there (run dir/bin/tm, docs/SPEC.md §3.6), not from the bin dir.
var (
	runDirsMu sync.Mutex
	runDirs   []string
)

func noteRunDir(dir string) {
	runDirsMu.Lock()
	defer runDirsMu.Unlock()
	runDirs = append(runDirs, dir)
}

// killUnderRuns is killUnder for every run dir this run made.
func killUnderRuns() []string {
	runDirsMu.Lock()
	defer runDirsMu.Unlock()
	var killed []string
	for _, d := range runDirs {
		killed = append(killed, killUnder(d)...)
	}
	return killed
}

func noteFailed(name, artifacts string) {
	failedMu.Lock()
	defer failedMu.Unlock()
	if artifacts != "" {
		name += " (artifacts: " + artifacts + ")"
	}
	failed = append(failed, name)
}

// sweepStale kills the processes of dead runs and removes their binaries
// and run dirs. A bin dir without an owner file (older harness, or a run
// still building) counts as dead only after an hour.
func sweepStale() {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), binPrefix+"*"))
	for _, d := range dirs {
		if !staleOwner(d) {
			continue
		}
		killUnder(d)
		os.RemoveAll(d)
	}
	// Run dirs of dead runs: no live server, and old enough that no
	// running test is between creating one and starting its server.
	runs, _ := filepath.Glob("/tmp/tme2e*")
	for _, d := range runs {
		fi, err := os.Stat(d)
		if err != nil || time.Since(fi.ModTime()) < 10*time.Minute {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(d, "server.pid"))
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); !Alive(pid) {
			os.RemoveAll(d)
		}
	}
}

func staleOwner(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, ownerFile))
	if err != nil {
		fi, err := os.Stat(dir)
		return err == nil && time.Since(fi.ModTime()) > time.Hour
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid != os.Getpid() && !Alive(pid)
}

// killUnder SIGKILLs every process whose executable lies in dir and
// returns what it killed.
func killUnder(dir string) []string {
	out, err := exec.Command("ps", "-e", "-o", "pid=", "-o", "args=").Output()
	if err != nil {
		return nil
	}
	var killed []string
	prefix := dir + string(filepath.Separator)
	for _, l := range strings.Split(string(out), "\n") {
		pidStr, args, ok := strings.Cut(strings.TrimSpace(l), " ")
		if !ok {
			continue
		}
		args = strings.TrimSpace(args)
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid == os.Getpid() || !strings.HasPrefix(args, prefix) {
			continue
		}
		if syscall.Kill(pid, syscall.SIGKILL) == nil {
			killed = append(killed, strconv.Itoa(pid)+" "+args)
		}
	}
	return killed
}
