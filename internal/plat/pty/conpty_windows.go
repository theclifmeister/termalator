package pty

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// conptyAPI is a ConPTY implementation: the one Windows has (kernel32
// and the conhost.exe of the Windows build), or the one shipped with tm
// (conpty.dll and OpenConsole.exe from microsoft/terminal, next to
// tm.exe; scripts/release/conpty.sh). The shipped one gives every Windows
// version the same behaviour: older conhost (Windows 10, Server 2022)
// swallows OSC sequences and DECSET modes it doesn't know.
type conptyAPI struct {
	createP, resizeP, closeP, releaseP *windows.LazyProc
}

// conpty is the shipped ConPTY when it is next to the executable, else
// the system's.
var conpty = sync.OnceValue(func() *conptyAPI {
	if exe, err := os.Executable(); err == nil {
		if api := shippedConpty(filepath.Dir(exe)); api != nil {
			return api
		}
	}
	return systemConpty()
})

// shippedConpty is the ConPTY in dir: conpty.dll and the OpenConsole.exe
// it starts, both from microsoft/terminal. Nil if either is missing or
// the DLL doesn't load.
func shippedConpty(dir string) *conptyAPI {
	dll := filepath.Join(dir, "conpty.dll")
	for _, f := range []string{dll, filepath.Join(dir, "OpenConsole.exe")} {
		if _, err := os.Stat(f); err != nil {
			return nil
		}
	}
	d := windows.NewLazyDLL(dll)
	if d.Load() != nil {
		return nil
	}
	api := &conptyAPI{
		createP: d.NewProc("CreatePseudoConsole"), resizeP: d.NewProc("ResizePseudoConsole"),
		closeP: d.NewProc("ClosePseudoConsole"), releaseP: d.NewProc("ReleasePseudoConsole")}
	for _, p := range []*windows.LazyProc{api.createP, api.resizeP, api.closeP, api.releaseP} {
		if p.Find() != nil {
			return nil
		}
	}
	return api
}

// systemConpty is Windows' own ConPTY. ReleasePseudoConsole is newer
// than the rest and may be missing.
func systemConpty() *conptyAPI {
	k := windows.NewLazySystemDLL("kernel32.dll")
	api := &conptyAPI{createP: k.NewProc("CreatePseudoConsole"), resizeP: k.NewProc("ResizePseudoConsole"),
		closeP: k.NewProc("ClosePseudoConsole"), releaseP: k.NewProc("ReleasePseudoConsole")}
	if api.releaseP.Find() != nil {
		api.releaseP = nil
	}
	return api
}

// coord is a COORD passed by value: X in the low word.
func coord(cols, rows uint16) uintptr { return uintptr(cols) | uintptr(rows)<<16 }

func (a *conptyAPI) create(cols, rows uint16, in, out windows.Handle) (windows.Handle, error) {
	var hpc windows.Handle
	r, _, _ := a.createP.Call(coord(cols, rows), uintptr(in), uintptr(out), 0, uintptr(unsafe.Pointer(&hpc)))
	if r != 0 {
		return 0, windows.Errno(r & 0xffff) // an HRESULT from a Win32 error
	}
	return hpc, nil
}

func (a *conptyAPI) resize(hpc windows.Handle, cols, rows uint16) error {
	if r, _, _ := a.resizeP.Call(uintptr(hpc), coord(cols, rows)); r != 0 {
		return windows.Errno(r & 0xffff)
	}
	return nil
}

func (a *conptyAPI) close(hpc windows.Handle) { a.closeP.Call(uintptr(hpc)) }

// release lets conhost exit once the last process on the console has
// gone, which ends the output. Without ReleasePseudoConsole conhost
// stays until ClosePseudoConsole, and the session closes the console
// shortly after its process exits instead.
func (a *conptyAPI) release(hpc windows.Handle) {
	if a.releaseP != nil {
		a.releaseP.Call(uintptr(hpc))
	}
}

// focusIn is a focus report: the terminal has the focus.
var focusIn = []byte("\x1b[I")

// filter takes ConPTY's own artifacts out of its output:
//
//   - Until the program sets a title, conhost sets the console's, the
//     exe path, and ConPTY passes it on (OSC 0). The filter drops it, so
//     a session has no title until its program sets one, as on Unix.
//   - ConPTY asks for focus reports (DECSET 1004) at start, to pass focus
//     on to the program, and has none until the terminal sends one. The
//     filter answers the first request with "focused" (answer); attach
//     clients report focus changes from then on, since their mirrors see
//     the mode on.
//
// Sequences split across reads are held back until they are complete or
// can't match.
type filter struct {
	drop   [][]byte // sequences taken out
	watch  []byte   // a sequence passed on that calls answer, once
	answer func()
	held   []byte
}

func newFilter(exe string, answer func()) filter {
	f := filter{watch: []byte("\x1b[?1004h"), answer: answer}
	for _, osc := range []string{"0", "2"} {
		for _, st := range []string{"\x07", "\x1b\\"} {
			f.drop = append(f.drop, []byte("\x1b]"+osc+";"+exe+st))
		}
	}
	return f
}

// feed appends p, filtered, to dst.
func (f *filter) feed(dst, p []byte) []byte {
	buf := p
	if len(f.held) > 0 {
		buf = append(f.held, p...)
		f.held = nil
	}
	for i := 0; i < len(buf); {
		j := bytes.IndexByte(buf[i:], 0x1b)
		if j < 0 {
			return append(dst, buf[i:]...)
		}
		dst = append(dst, buf[i:i+j]...)
		i += j
		n, partial := f.match(buf[i:])
		switch {
		case partial:
			f.held = append([]byte(nil), buf[i:]...)
			return dst
		case n > 0: // dropped
			i += n
		default:
			dst = append(dst, buf[i])
			i++
		}
	}
	return dst
}

// flush hands out what is held back, at the end of the output.
func (f *filter) flush(dst []byte) []byte {
	dst = append(dst, f.held...)
	f.held = nil
	return dst
}

// match looks at the sequence starting at b[0] (ESC): n is the length of
// a sequence to drop there, partial says b ends inside one that may still
// match. The watched sequence is passed on (n = 0).
func (f *filter) match(b []byte) (n int, partial bool) {
	if f.watch != nil {
		if hasPrefixFold(b, f.watch) {
			f.watch = nil
			f.answer()
		} else if len(b) < len(f.watch) && hasPrefixFold(f.watch, b) {
			return 0, true
		}
	}
	for _, d := range f.drop {
		if hasPrefixFold(b, d) {
			return len(d), false
		}
		if len(b) < len(d) && hasPrefixFold(d, b) {
			partial = true
		}
	}
	return 0, partial
}

// hasPrefixFold is bytes.HasPrefix, ignoring ASCII case (Windows paths).
func hasPrefixFold(b, prefix []byte) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i, c := range prefix {
		if lower(b[i]) != lower(c) {
			return false
		}
	}
	return true
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
