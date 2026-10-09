package service

import "testing"

// memStore is a fake Windows startup list.
type memStore map[string]string

func (m memStore) Set(name, command string) error { m[name] = command; return nil }
func (m memStore) Get(name string) (string, bool, error) {
	v, ok := m[name]
	return v, ok, nil
}
func (m memStore) Remove(name string) error { delete(m, name); return nil }

func TestRunKey(t *testing.T) {
	c := Config{GOOS: "windows", Bin: `C:\Users\me\AppData\Local\Programs\terminatr\tm.exe`, Store: memStore{}}
	f, err := c.File()
	if err != nil || f != `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\dev.terminatr.server` {
		t.Fatalf("File = %q, %v", f, err)
	}
	// Install is idempotent, replaces the value, and Uninstall removes it.
	for range 2 {
		if _, err := c.Install(); err != nil {
			t.Fatal(err)
		}
	}
	want := `"C:\Users\me\AppData\Local\Programs\terminatr\tm.exe" server start`
	if got, ok, _ := c.Store.Get(Label); !ok || got != want {
		t.Fatalf("Run value = %q (%v), want %q", got, ok, want)
	}
	if data, err := c.Render(); err != nil || string(data) != want+"\n" {
		t.Errorf("Render = %q, %v", data, err)
	}
	if _, err := c.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Store.Get(Label); ok {
		t.Error("Run value left after Uninstall")
	}
	if _, err := c.Uninstall(); err != nil {
		t.Errorf("uninstalling what isn't installed: %v", err)
	}
}

func TestRunKeyOtherHome(t *testing.T) {
	c := Config{GOOS: "windows", Bin: `C:\tm dir\tm.exe`, Home: `D:\data & co`, Store: memStore{}}
	if _, err := c.Install(); err == nil {
		t.Error("a home with & installed")
	}
	c.Home = `D:\my data`
	if _, err := c.Install(); err != nil {
		t.Fatal(err)
	}
	want := `cmd.exe /d /s /c "set "TERMINATR_HOME=D:\my data"&& "C:\tm dir\tm.exe" server start"`
	if got, _, _ := c.Store.Get(c.JobLabel()); got != want {
		t.Errorf("Run value = %q, want %q", got, want)
	}
	if c.JobLabel() == Label {
		t.Error("another home shares the default's value name")
	}
}
