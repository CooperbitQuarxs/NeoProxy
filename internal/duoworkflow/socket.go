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
// sees it: sends, and a close that reports through a "close" event. Neither
// waits on the network: Send queues the frame, failing only when the socket
// is not open, as ws.send throws then.
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

// send is ws.send: it fails when the socket is not open.
func (s *socket) send(data string) error {
	return s.conn.Send(data)
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
		conn := &gorillaConn{wake: make(chan struct{}, 1)}
		go conn.run(dialer, url, headers, deliver)
		return conn
	}
}

// gorillaConn is a WebSocket whose frames a writer goroutine sends in
// order, so no caller waits on GitLab reading them. writeErr is a frame's
// failed write, which the reader reports as the socket's error.
type gorillaConn struct {
	mu       sync.Mutex
	conn     *websocket.Conn
	closed   bool
	writing  bool
	queue    []string
	writeErr error
	wake     chan struct{}
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
	go c.write(conn)
	deliver(SocketEvent{Type: "open"})
	for {
		_, data, errRead := conn.ReadMessage()
		if errRead != nil {
			// The connection ends with its reader, which also releases a
			// write GitLab stopped taking.
			c.mu.Lock()
			c.closed = true
			writeErr := c.writeErr
			c.mu.Unlock()
			_ = conn.Close()
			c.signal()
			if writeErr != nil {
				deliver(SocketEvent{Type: "error", Err: writeErr})
			}
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

func (c *gorillaConn) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// write sends the queued frames in order and, once the socket is closed
// and drained, a close frame without a status, as ws.close() does; then it
// drops the connection, and the reader reports the close.
func (c *gorillaConn) write(conn *websocket.Conn) {
	for {
		c.mu.Lock()
		for len(c.queue) == 0 && !c.closed {
			c.mu.Unlock()
			<-c.wake
			c.mu.Lock()
		}
		if len(c.queue) == 0 {
			c.mu.Unlock()
			_ = conn.WriteControl(websocket.CloseMessage, []byte{}, time.Time{})
			_ = conn.Close()
			return
		}
		data := c.queue[0]
		c.queue = c.queue[1:]
		c.writing = true
		c.mu.Unlock()
		err := conn.WriteMessage(websocket.TextMessage, []byte(data))
		c.mu.Lock()
		c.writing = false
		if err != nil && !c.closed {
			// A frame that did not go out fails the socket, unless its
			// close already dropped the connection under it.
			c.writeErr, c.closed = err, true
		}
		c.mu.Unlock()
		if err != nil {
			_ = conn.Close()
			return
		}
	}
}

func (c *gorillaConn) Send(data string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.closed {
		return errors.New("duo workflow socket is not open")
	}
	c.queue = append(c.queue, data)
	c.signal()
	return nil
}

// Close closes the socket once. With every frame sent, the writer sends the
// close frame, as ws.close() does. A frame still queued or being written
// would hold that close back for as long as GitLab is not reading it, so
// the connection is dropped instead: the runtime closes with frames unsent
// only when the request ends or the session is evicted.
func (c *gorillaConn) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	conn, unsent := c.conn, c.writing || len(c.queue) > 0
	c.queue = nil
	c.mu.Unlock()
	if conn != nil && unsent {
		_ = conn.Close()
	}
	c.signal()
}
