package duoworkflow

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

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
