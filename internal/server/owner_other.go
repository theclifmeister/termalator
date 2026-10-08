//go:build !unix

package server

import "io/fs"

// Not yet ported: the server runs only on Unix. This keeps the package
// building elsewhere.

func fileOwner(fi fs.FileInfo) (int, bool) { return 0, false }
