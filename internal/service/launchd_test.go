package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJobLabel(t *testing.T) {
	c, _ := config(t, "darwin")
	other := c
	other.Home = "/data/other"
	if c.JobLabel() == other.JobLabel() || !strings.HasPrefix(c.JobLabel(), Label+".") {
		t.Fatalf("labels %s %s", c.JobLabel(), other.JobLabel())
	}
	c.Home = ""
	if c.JobLabel() != Label {
		t.Fatalf("default home: %s", c.JobLabel())
	}
}

func TestWanted(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if !Wanted("darwin", env("")) || Wanted("darwin", env("off")) || Wanted("linux", env("")) {
		t.Fatal("Wanted")
	}
}

func TestLaunchEnv(t *testing.T) {
	env := []string{"PATH=/opt/homebrew/bin:/usr/bin", "LANG=en_GB.UTF-8", "GH_HOST=example",
		"SSH_CONNECTION=1 2 3 4", "SSH_TTY=/dev/ttys001", "SECURITYSESSIONID=186a5", "PWD=/x", "SHLVL=2",
		"SSH_AUTH_SOCK=/tmp/agent", "TERMINATR_HOME=/h", "XPC_SERVICE_NAME=0", "TMPDIR=/var/folders/x/"}
	get := func(k string) string {
		for _, kv := range env {
			if a, b, _ := strings.Cut(kv, "="); a == k {
				return b
			}
		}
		return ""
	}
	got := strings.Join(LaunchEnv(env, get), " ")
	if got != "PATH=/opt/homebrew/bin:/usr/bin LANG=en_GB.UTF-8 GH_HOST=example TERMINATR_HOME=/h" {
		t.Fatalf("over SSH: %s", got)
	}
	// A local terminal's agent socket stays (a password manager's, say).
	local := func(k string) string {
		if strings.HasPrefix(k, "SSH_") && k != "SSH_AUTH_SOCK" {
			return ""
		}
		return get(k)
	}
	if got := LaunchEnv(env, local); !strings.Contains(strings.Join(got, " "), "SSH_AUTH_SOCK=/tmp/agent") {
		t.Fatalf("locally: %s", got)
	}
}

func TestLaunchFile(t *testing.T) {
	c, _ := config(t, "darwin")
	c.Env = []string{"TM_LAUNCH_TEST=a=b", "TM_LAUNCH_EMPTY="}
	if err := c.writeLaunchFile(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(c.LaunchFile()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("launch file: %v %v", fi, err)
	}
	t.Setenv("TM_LAUNCH_TEST", "old")
	if err := ApplyLaunchFile(c.LaunchFile()); err != nil {
		t.Fatal(err)
	}
	if v, ok := os.LookupEnv("TM_LAUNCH_EMPTY"); os.Getenv("TM_LAUNCH_TEST") != "a=b" || !ok || v != "" {
		t.Fatalf("applied: %q %q", os.Getenv("TM_LAUNCH_TEST"), v)
	}
	os.Unsetenv("TM_LAUNCH_EMPTY")
	if err := ApplyLaunchFile(filepath.Join(t.TempDir(), "none.json")); err != nil {
		t.Fatalf("missing file: %v", err)
	}
}

func TestStart(t *testing.T) {
	c, f := config(t, "darwin")
	c.Env = []string{"PATH=/usr/bin"}
	job := "gui/501/" + c.JobLabel()
	onDemand := filepath.Join(c.RunDir, c.JobLabel()+".plist")
	check := func(what string, want ...string) {
		t.Helper()
		if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: calls\n%s\nwant\n%s", what, strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
		}
		f.calls = nil
	}

	// Nobody at the console: nothing written, nothing loaded.
	f.fail = map[string]bool{"launchctl print gui/501": true}
	if err := c.Start(); !errors.Is(err, ErrNoConsole) {
		t.Fatalf("no console: %v", err)
	}
	check("no console", "launchctl print gui/501")
	if _, err := os.Stat(c.LaunchFile()); err == nil {
		t.Fatal("no console: launch file written")
	}

	// Not loaded: the on-demand job, which doesn't start at load.
	f.fail = map[string]bool{"launchctl print " + job: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	check("first start", "launchctl print gui/501", "launchctl print "+job,
		"launchctl bootstrap gui/501 "+onDemand, "launchctl kickstart "+job)
	data, _ := os.ReadFile(onDemand)
	if !strings.Contains(string(data), "<key>RunAtLoad</key>\n\t<false/>") {
		t.Fatalf("on-demand plist:\n%s", data)
	}
	if _, err := os.Stat(c.LaunchFile()); err != nil {
		t.Fatal("no launch file")
	}

	// Loaded and unchanged: just kickstart.
	f.fail = nil
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	check("loaded", "launchctl print gui/501", "launchctl print "+job, "launchctl kickstart "+job)

	// tm was upgraded: its path changed, so the job is reloaded.
	c.Bin = "/opt/homebrew/Cellar/terminatr/9.9.9/bin/tm"
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	check("upgraded", "launchctl print gui/501", "launchctl print "+job, "launchctl bootout "+job,
		"launchctl bootstrap gui/501 "+onDemand, "launchctl kickstart "+job)

	// With the login service installed, its plist is the job.
	installed, _ := c.File()
	os.MkdirAll(filepath.Dir(installed), 0o755)
	os.WriteFile(installed, []byte("old"), 0o644)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	check("installed", "launchctl print gui/501", "launchctl print "+job, "launchctl bootout "+job,
		"launchctl bootstrap gui/501 "+installed, "launchctl kickstart "+job)
	if data, _ := os.ReadFile(installed); !strings.Contains(string(data), "<key>RunAtLoad</key>\n\t<true/>") {
		t.Fatalf("installed plist:\n%s", data)
	}

	if c.GOOS = "linux"; !errors.Is(c.Start(), ErrUnsupported) {
		t.Fatal("linux")
	}
}
