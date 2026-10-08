//go:build !windows

package shell

// ShortPath is p's 8.3 short form on Windows; elsewhere it is p.
func ShortPath(p string) string { return p }
