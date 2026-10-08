package shell

import "golang.org/x/sys/windows"

// ShortPath is p's 8.3 short form (C:/PROGRA~1/…), which needs no quoting
// in any shell, or p when the volume keeps no short names.
func ShortPath(p string) string {
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, 260)
	for {
		n, err := windows.GetShortPathName(in, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return p
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n])
		}
		buf = make([]uint16, n)
	}
}
