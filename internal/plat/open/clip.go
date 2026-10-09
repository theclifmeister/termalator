package open

import (
	"bytes"
	"errors"
	"os/exec"
	"runtime"
	"unicode/utf16"
)

// Clipboard puts text on this machine's clipboard through the platform's
// tool: pbcopy, clip.exe (Windows, and from WSL), wl-copy or xclip. It
// waits for the tool to finish.
func Clipboard(text string) error {
	have := func(n string) bool { _, err := exec.LookPath(n); return err == nil }
	name, args, in := clipCommand(runtime.GOOS, text, wsl(), have)
	if name == "" {
		return errors.New("no clipboard tool found (pbcopy, clip.exe, wl-copy, xclip)")
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = bytes.NewReader(in)
	return cmd.Run()
}

// clipCommand picks the clipboard tool for goos and the bytes to feed it;
// the name is empty when none is available. clip.exe reads UTF-16 with a
// byte order mark as Unicode, else the console's code page.
func clipCommand(goos, text string, inWSL bool, have func(string) bool) (string, []string, []byte) {
	switch {
	case goos == "darwin":
		return "pbcopy", nil, []byte(text)
	case goos == "windows" || inWSL && have("clip.exe"):
		return "clip", nil, utf16le(text)
	case have("wl-copy"):
		return "wl-copy", nil, []byte(text)
	case have("xclip"):
		return "xclip", []string{"-selection", "clipboard"}, []byte(text)
	}
	return "", nil, nil
}

func utf16le(s string) []byte {
	u := append([]uint16{0xFEFF}, utf16.Encode([]rune(s))...)
	b := make([]byte, 0, 2*len(u))
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}
