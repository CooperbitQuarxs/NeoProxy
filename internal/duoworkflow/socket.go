package duoworkflow

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// SocketEvent is one WebSocket event: "open", "message" (Data), "error"
// (Err) or "close" (Code, Reason).
type SocketEvent struct {
	Type   string
	Data   string
	Err    error
	Code   int
	Reason string
}

// Conn is one Duo Workflow WebSocket as NeoPi's GitLabDuoWorkflowWebSocketLike
// sees it: sends, and a close that reports through a "close" event.
type Conn interface {
	Send(data string) error
	Close()
}

// Dialer opens the workflow socket and reports its events through deliver,
// one at a time and never from the goroutine calling Dialer, Send or Close.
type Dialer func(url string, headers map[string]string, deliver func(SocketEvent)) Conn

// socket is the event-handler side of a Conn. Its handlers run under the
// session's lock, which stands in for NeoPi's event loop: each run of the
// socket loop installs its own, and they stay installed after it settles,
// as NeoPi's onmessage/onclose do.
type socket struct {
	conn         Conn
	loop         *sync.Mutex
	onOpen       func()
	onMessage    func(data string)
	onError      func(err error)
	onClose      func(code int, reason string)
	closed       bool
	clientClosed bool
}

func (s *socket) deliver(event SocketEvent) {
	s.loop.Lock()
	defer s.loop.Unlock()
	switch event.Type {
	case "open":
		if s.onOpen != nil {
			s.onOpen()
		}
	case "message":
		if s.onMessage != nil {
			s.onMessage(event.Data)
		}
	case "error":
		if s.onError != nil {
			s.onError(event.Err)
		}
	case "close":
		s.closed = true
		if s.onClose != nil {
			s.onClose(event.Code, event.Reason)
		}
	}
}

// send is ws.send: a socket that already closed drops the frame.
func (s *socket) send(data string) {
	_ = s.conn.Send(data)
}

// close is ws.close(); closing twice is closing once.
func (s *socket) close() {
	if s.clientClosed {
		return
	}
	s.clientClosed = true
	s.conn.Close()
}

// WebSocketDialer opens workflow sockets with dialer. The handshake is the
// dialer's to bound; once open, the socket waits on GitLab without a
// deadline, where NeoPi gives up after 90 s of silence.
func WebSocketDialer(dialer *websocket.Dialer) Dialer {
	return func(url string, headers map[string]string, deliver func(SocketEvent)) Conn {
		conn := &gorillaConn{}
		go conn.run(dialer, url, headers, deliver)
		return conn
	}
}

type gorillaConn struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	closed bool
}

func (c *gorillaConn) run(dialer *websocket.Dialer, url string, headers map[string]string, deliver func(SocketEvent)) {
	header := http.Header{}
	for name, value := range headers {
		header.Set(name, value)
	}
	conn, resp, err := dialer.DialContext(context.Background(), url, header)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		deliver(SocketEvent{Type: "error", Err: err})
		deliver(SocketEvent{Type: "close", Code: websocket.CloseAbnormalClosure})
		return
	}
	c.mu.Lock()
	c.conn = conn
	closed := c.closed
	c.mu.Unlock()
	if closed {
		_ = conn.Close()
		deliver(SocketEvent{Type: "close", Code: websocket.CloseAbnormalClosure})
		return
	}
	deliver(SocketEvent{Type: "open"})
	for {
		_, data, errRead := conn.ReadMessage()
		if errRead != nil {
			event := SocketEvent{Type: "close", Code: websocket.CloseAbnormalClosure}
			var closeErr *websocket.CloseError
			if errors.As(errRead, &closeErr) {
				event.Code, event.Reason = closeErr.Code, closeErr.Text
			}
			deliver(event)
			return
		}
		deliver(SocketEvent{Type: "message", Data: string(data)})
	}
}

func (c *gorillaConn) Send(data string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.closed {
		return errors.New("duo workflow socket is not open")
	}
	return c.conn.WriteMessage(websocket.TextMessage, []byte(data))
}

// Close sends a close frame without a status, as ws.close() does, and drops
// the connection; the reader then reports the close.
func (c *gorillaConn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.conn != nil {
		_ = c.conn.WriteControl(websocket.CloseMessage, []byte{}, time.Time{})
		_ = c.conn.Close()
	}
}
