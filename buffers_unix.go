//go:build !windows

package wireproxy

import (
	"net"
	"syscall"
)

func readBufferSize(c *net.TCPConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var size int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		size, serr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	}); err != nil {
		return 0, err
	}
	return size, serr
}
