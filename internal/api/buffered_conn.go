package api

import (
	"bufio"
	"crypto/tls"
	"net"
)

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c == nil {
		return 0, net.ErrClosed
	}
	if c.reader == nil {
		return c.Conn.Read(p)
	}
	return c.reader.Read(p)
}

// net/http treats ConnectionState's presence as TLS, even for a zero state.
// Plain buffered connections must not expose it.
type bufferedTLSConn struct {
	bufferedConn
}

func (c *bufferedTLSConn) ConnectionState() tls.ConnectionState {
	return c.Conn.(*tls.Conn).ConnectionState()
}
