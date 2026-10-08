//go:build !darwin && !linux

package server

import (
	"errors"
	"net"
)

// peerCred is not ported here: the server refuses every connection.
func peerCred(c *net.UnixConn) (uid int, pid int, err error) {
	return 0, 0, errors.ErrUnsupported
}
