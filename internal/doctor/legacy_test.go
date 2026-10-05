package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacy(t *testing.T) {
	d := testDeps(t)
	user := t.TempDir()
	t.Setenv("HOME", user)
	if cs := Legacy(d); cs != nil {
		t.Fatalf("clean install: %+v", cs)
	}
	// Agent files a v0.1.0 server generated, and its login service.
	hooks := filepath.Join(d.Paths.RunDir, "s", "s-1", "claude-plugin", "hooks", "hooks.json")
	os.MkdirAll(filepath.Dir(hooks), 0o700)
	os.WriteFile(hooks, []byte(`{"command": "\"`+user+`/.termalator/server-bin/tm-x\" hook"}`), 0o600)
	unit := filepath.Join(user, ".config", "systemd", "user", "termalator.service")
	os.MkdirAll(filepath.Dir(unit), 0o700)
	os.WriteFile(unit, []byte("[Unit]\n"), 0o644)
	d.UserHome = user
	var ran []string
	d.ServiceRun = func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}
	installed := false
	d.InstallService = func() error { installed = true; return nil }

	cs := Legacy(d)
	files, svc := find(cs, "agent files"), find(cs, "service")
	if len(files) != 1 || files[0].Status != Warn || files[0].Fix == nil {
		t.Fatalf("agent files: %+v", cs)
	}
	if len(svc) != 1 || svc[0].Status != Warn || svc[0].Fix == nil {
		t.Fatalf("service: %+v", cs)
	}
	for _, f := range Fixes(cs) {
		if err := f.Apply(); err != nil {
			t.Fatalf("%s: %v", f.Desc, err)
		}
	}
	if b, _ := os.ReadFile(hooks); !strings.Contains(string(b), d.Paths.Home+"/server-bin/tm-x") {
		t.Errorf("hooks.json not rewritten: %s", b)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) || !installed {
		t.Errorf("service not replaced (installed %v, ran %q)", installed, ran)
	}
	if cs := Legacy(d); cs != nil {
		t.Errorf("after --fix: %+v", cs)
	}
}
