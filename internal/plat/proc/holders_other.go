//go:build !windows

package proc

import "errors"

// Holders lists the processes that hold any of paths open. Only Windows
// can say (its Restart Manager); a Unix file in use is no obstacle to
// replacing it.
func Holders(paths ...string) ([]Holder, error) { return nil, errors.ErrUnsupported }
