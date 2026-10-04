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

// TestRowsSkipDim blanks faint cells in the second result only.
func TestRowsSkipDim(t *testing.T) {
	term, err := New(20, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Write([]byte("❯ \x1b[2mghost\x1b[22m x\r\n漢字 ok"))
	plain, noDim, err := term.Rows()
	if err != nil {
		t.Fatal(err)
	}
	if plain[0] != "❯ ghost x" || noDim[0] != "❯       x" {
		t.Fatalf("row 0: plain %q, noDim %q", plain[0], noDim[0])
	}
	if plain[1] != "漢字 ok" || noDim[1] != "漢字 ok" {
		t.Fatalf("row 1: plain %q, noDim %q", plain[1], noDim[1])
	}
	if len(plain) != 3 || plain[2] != "" {
		t.Fatalf("rows: %q", plain)
	}
}
