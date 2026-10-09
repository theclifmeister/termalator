//go:build !windows

package fsx

// moveAside does nothing: the rename in SwapIn replaces the file while
// its processes go on running it.
func moveAside(string) (string, error) { return "", nil }
