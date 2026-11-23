package wireproxy

// wirefork: receive buffer of connections accepted on the local proxy ports.
// Without a limit the operating system grows it to several MB (the connection
// is local and fast), so programs can hand over far more data than the tunnel
// carries. Together with the TCP send buffer (netstack.SetTCPSendBuffer) this
// keeps the data waiting per connection small.

import (
	"log"
	"net"
	"runtime"
	"sync"
)

// LocalBufferSize is the receive buffer in bytes for accepted proxy
// connections; 0 leaves the system default.
var LocalBufferSize int

var reportBuffer sync.Once

func setLocalBuffer(c net.Conn) {
	tc, ok := c.(*net.TCPConn)
	if !ok || LocalBufferSize <= 0 {
		return
	}
	// Linux doubles the requested value (bookkeeping overhead), so request half.
	req := LocalBufferSize
	if runtime.GOOS == "linux" {
		req /= 2
	}
	err := tc.SetReadBuffer(req)
	reportBuffer.Do(func() {
		if err != nil {
			log.Printf("wirefork: local proxy receive buffer: %v", err)
			return
		}
		applied, rerr := readBufferSize(tc)
		if rerr != nil {
			log.Printf("wirefork: local proxy receive buffer %d bytes requested (applied size unknown: %v)", req, rerr)
			return
		}
		log.Printf("wirefork: local proxy receive buffer %d bytes requested, %d applied", req, applied)
	})
}

type bufferListener struct{ net.Listener }

func (l bufferListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		setLocalBuffer(c)
	}
	return c, err
}

// withLocalBuffer applies LocalBufferSize to every accepted connection.
func withLocalBuffer(l net.Listener) net.Listener {
	if LocalBufferSize <= 0 {
		return l
	}
	return bufferListener{l}
}
