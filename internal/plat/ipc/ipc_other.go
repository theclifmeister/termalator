//go:build !unix && !windows

package ipc

import (
	"context"
	"errors"
	"net"
	"os"
)

// Not ported: every call fails with errors.ErrUnsupported.

func Listen(a Addr) (net.Listener, error)                { return nil, errors.ErrUnsupported }
func Dial(ctx context.Context, a Addr) (net.Conn, error) { return nil, errors.ErrUnsupported }
func IsAbsent(err error) bool                            { return false }
func PeerOf(c net.Conn) (Peer, error)                    { return Peer{}, errors.ErrUnsupported }
func ShortDir() string                                   { return os.TempDir() }
