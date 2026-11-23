//go:build windows

package wireproxy

import (
	"net"
	"syscall"
	"unsafe"
)

func readBufferSize(c *net.TCPConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var size int32
	var serr error
	if err := raw.Control(func(fd uintptr) {
		n := int32(unsafe.Sizeof(size))
		serr = syscall.Getsockopt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, (*byte)(unsafe.Pointer(&size)), &n)
	}); err != nil {
		return 0, err
	}
	return int(size), serr
}
