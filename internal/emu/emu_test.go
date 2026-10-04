package emu

import "testing"

// TestLinked proves libghostty-vt is linked and parses VT input.
func TestLinked(t *testing.T) {
	term, err := New(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	if _, err := term.Write([]byte("Hello, \x1b[1;32mworld\x1b[0m!\r\n")); err != nil {
		t.Fatal(err)
	}
	got, err := term.PlainText()
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hello, world!" {
		t.Fatalf("PlainText() = %q, want %q", got, "Hello, world!")
	}
}
