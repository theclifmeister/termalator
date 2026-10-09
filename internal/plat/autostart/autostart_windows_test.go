//go:build windows

package autostart

import "testing"

func TestRunKey(t *testing.T) {
	const name = "terminatr-test-autostart"
	s := User()
	t.Cleanup(func() { s.Remove(name) })
	if _, ok, err := s.Get(name); err != nil || ok {
		t.Fatalf("before: %v %v", ok, err)
	}
	cmd := `"C:\Program Files\tm\tm.exe" server start`
	if err := s.Set(name, cmd); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.Get(name); err != nil || !ok || got != cmd {
		t.Fatalf("Get = %q %v %v", got, ok, err)
	}
	if err := s.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(name); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
	if _, ok, _ := s.Get(name); ok {
		t.Fatal("still there")
	}
}
