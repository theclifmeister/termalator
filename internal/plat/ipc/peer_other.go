//go:build unix && !darwin && !linux

package ipc

import "errors"

// peerCred is not ported here: every PeerOf fails, so the server refuses
// every connection.
func peerCred(fd int) (uid, pid int, err error) {
	return 0, 0, errors.ErrUnsupported
}
