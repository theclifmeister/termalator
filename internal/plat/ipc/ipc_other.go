//go:build !unix

package ipc

import (
	"context"
	"errors"
	"net"
)

// Not yet ported: every call fails with errors.ErrUnsupported. The
// Windows port replaces this with named pipes (doc in ipc.go).

func Listen(a Addr) (net.Listener, error)                { return nil, errors.ErrUnsupported }
func Dial(ctx context.Context, a Addr) (net.Conn, error) { return nil, errors.ErrUnsupported }
func IsAbsent(err error) bool                            { return false }
func PeerOf(c net.Conn) (Peer, error)                    { return Peer{}, errors.ErrUnsupported }
