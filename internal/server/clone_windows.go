package server

import "errors"

// pinExt is the pin's extension: Windows runs only a file named .exe.
const pinExt = ".exe"

// companions are pinned beside tm.exe: the ConPTY that plat/pty loads
// from beside the running exe (the release ships it).
var companions = []string{"conpty.dll", "OpenConsole.exe"}

// cloneFile refuses, so the pin is a copy: a hard link to a running exe
// shares its image, which Windows won't open for the write that syncing
// the new pin needs, nor let an upgrade replace.
func cloneFile(src, dst string) error {
	return errors.New("no clone on Windows")
}
