package duoworkflow

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// gatedConn is a client connection whose writes the test fails or holds
// until the connection is closed.
type gatedConn struct {
	net.Conn
	mu        sync.Mutex
	mode      string
	released  chan struct{}
	closeOnce sync.Once
}

func (c *gatedConn) set(mode string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = mode
}

func (c *gatedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	mode := c.mode
	c.mu.Unlock()
	switch mode {
	case "fail":
		return 0, errors.New("write refused")
	case "hold":
		<-c.released
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}

func (c *gatedConn) Close() error {
	c.closeOnce.Do(func() { close(c.released) })
	return c.Conn.Close()
}

// gatedSocket dials server through a gatedConn and waits for the open.
func gatedSocket(t *testing.T, server *httptest.Server) (Conn, *gatedConn, func(string) SocketEvent) {
	t.Helper()
	gated := make(chan *gatedConn, 1)
	dialer := &websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		wrapped := &gatedConn{Conn: conn, released: make(chan struct{})}
		gated <- wrapped
		return wrapped, nil
	}}
	events := make(chan SocketEvent, 16)
	conn := WebSocketDialer(dialer)("ws"+strings.TrimPrefix(server.URL, "http"), nil, func(event SocketEvent) { events <- event })
	// The bounds only keep a regression from hanging the suite.
	next := func(what string) SocketEvent {
		select {
		case event := <-events:
			return event
		case <-time.After(10 * time.Second):
			t.Fatalf("no %s event", what)
			return SocketEvent{}
		}
	}
	if event := next("open"); event.Type != "open" {
		t.Fatalf("first event %+v, want open", event)
	}
	return conn, <-gated, next
}

// TestWebSocketReportsAFrameThatFailsToGoOut fails the socket when a queued
// frame cannot be written: an error precedes the close, so the run fails
// rather than ending as a clean stop.
func TestWebSocketReportsAFrameThatFailsToGoOut(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		if _, err := upgrader.Upgrade(w, r, nil); err == nil {
			<-release
		}
	}))
	defer server.Close()
	defer close(release)
	conn, gated, next := gatedSocket(t, server)
	gated.set("fail")
	if err := conn.Send("{}"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if event := next("error"); event.Type != "error" || event.Err == nil {
		t.Fatalf("event after a failed write %+v, want an error", event)
	}
	if event := next("close"); event.Type != "close" {
		t.Fatalf("event after the error %+v, want close", event)
	}
}

// TestWebSocketReaderEndReleasesAHeldWrite drops the connection when its
// reader ends, so a write GitLab stopped taking does not outlive a peer
// that closed the WebSocket but kept its TCP connection.
func TestWebSocketReaderEndReleasesAHeldWrite(t *testing.T) {
	release := make(chan struct{})
	sendClose := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		<-sendClose
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"))
		<-release
	}))
	defer server.Close()
	defer close(release)
	conn, gated, next := gatedSocket(t, server)
	gated.set("hold")
	if err := conn.Send("{}"); err != nil {
		t.Fatalf("send: %v", err)
	}
	close(sendClose)
	if event := next("close"); event.Type != "close" || event.Code != websocket.CloseNormalClosure {
		t.Fatalf("event after the peer's close %+v, want its close", event)
	}
	select {
	case <-gated.released:
	case <-time.After(10 * time.Second):
		t.Fatal("the held write outlived the socket's reader")
	}
}

// TestWebSocketDoesNotWaitOnAStalledPeer keeps a peer that stopped reading
// from holding the runtime: a send queues its frame, and a close drops the
// connection under the frame GitLab never takes, after which the socket
// reports its close.
func TestWebSocketDoesNotWaitOnAStalledPeer(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		<-release
		_ = conn.Close()
	}))
	defer server.Close()
	defer close(release)

	events := make(chan SocketEvent, 16)
	conn := WebSocketDialer(&websocket.Dialer{})("ws"+strings.TrimPrefix(server.URL, "http"), nil, func(event SocketEvent) { events <- event })
	// The bounds below only keep a regression from hanging the suite.
	next := func(what string) SocketEvent {
		select {
		case event := <-events:
			return event
		case <-time.After(10 * time.Second):
			t.Fatalf("no %s event", what)
			return SocketEvent{}
		}
	}
	if event := next("open"); event.Type != "open" {
		t.Fatalf("first event %+v, want open", event)
	}

	sent := make(chan error, 1)
	go func() { sent <- conn.Send(strings.Repeat("x", 64<<20)) }()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Send waited on a peer that is not reading")
	}
	closed := make(chan struct{})
	go func() {
		conn.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited on the frame the peer is not reading")
	}
	if event := next("close"); event.Type != "close" {
		t.Fatalf("event after close %+v, want close", event)
	}
	if err := conn.Send("{}"); err == nil {
		t.Fatal("a closed socket accepted a frame")
	}
}
