//go:build darwin

package daemon

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID returns the user ID of the process at the other end of a Unix socket.
func peerUID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1, errors.New("not a Unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid := -1
	var cerr error
	err = raw.Control(func(fd uintptr) {
		var x *unix.Xucred
		x, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if cerr == nil {
			uid = int(x.Uid)
		}
	})
	if err != nil {
		return -1, err
	}
	return uid, cerr
}

// noCoreDumps keeps message data out of core files.
func noCoreDumps() { _ = unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}) }
