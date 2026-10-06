package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Stray is a loaded launchd job named like the server's that no server
// needs: a dev or test home's on-demand job left loaded after its run dir
// went away (docs/OPERATIONS.md, tm doctor).
type Stray struct {
	Label  string
	Plist  string // path launchd loaded it from; "" when unknown
	Reason string
}

// output runs a command and returns its stdout.
func (c Config) output(name string, args ...string) (string, error) {
	if c.Output != nil {
		return c.Output(name, args...)
	}
	b, err := exec.Command(name, args...).Output()
	return string(b), err
}

// Unload boots out this home's on-demand job and removes its plist, once
// its server has stopped, so a dev or test home leaves nothing loaded. It
// does nothing for the default home, or when the login service is
// installed (that one stays loaded).
func (c Config) Unload() error {
	if c.GOOS != "darwin" || c.Home == "" {
		return nil
	}
	if f, err := c.File(); err == nil {
		if _, err := os.Stat(f); err == nil {
			return nil
		}
	}
	c.run("launchctl", "bootout", c.target()) // an error means it wasn't loaded
	return removeFile(filepath.Join(c.RunDir, c.JobLabel()+".plist"))
}

// Bootout boots out the job with label, whichever home it belongs to.
func (c Config) Bootout(label string) error {
	return c.run("launchctl", "bootout", c.domain()+"/"+label)
}

// Strays lists the loaded dev.terminatr.server.<hash> jobs that aren't
// this home's, aren't running, and whose plist is gone, lives in a temp
// directory, or names a tm that no longer exists.
func (c Config) Strays() ([]Stray, error) {
	if c.GOOS != "darwin" {
		return nil, nil
	}
	list, err := c.output("launchctl", "list")
	if err != nil {
		return nil, err
	}
	var out []Stray
	for _, line := range strings.Split(list, "\n") {
		f := strings.Fields(line) // PID, last exit status, label
		if len(f) != 3 || !strings.HasPrefix(f[2], Label+".") || f[2] == c.JobLabel() || f[0] != "-" {
			continue
		}
		label := f[2]
		info, err := c.output("launchctl", "print", c.domain()+"/"+label)
		if err != nil {
			continue
		}
		plist, prog := printed(info, "path"), printed(info, "program")
		s := Stray{Label: label, Plist: plist}
		switch {
		case plist == "":
			continue
		case !exists(plist):
			s.Reason = "its plist " + plist + " is gone"
		case inTemp(plist):
			s.Reason = "its plist " + plist + " is in a temporary directory"
		case prog != "" && filepath.IsAbs(prog) && !exists(prog):
			s.Reason = "it runs " + prog + ", which is gone"
		default:
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// printed is the value of "key = value" in launchctl print output.
func printed(info, key string) string {
	for _, l := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), key+" = "); ok {
			return v
		}
	}
	return ""
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func inTemp(p string) bool {
	p = filepath.Clean(p)
	roots := []string{os.TempDir(), "/tmp", "/private/tmp", "/var/folders", "/private/var/folders"}
	for _, r := range roots {
		r = filepath.Clean(r)
		if strings.HasPrefix(p, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
