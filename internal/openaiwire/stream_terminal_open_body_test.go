package openaiwire

import (
	"context"
	"io"
	"net/http"
	"testing"
)

// terminalOpenBody delivers complete SSE frames but never EOF. A read beyond
// those frames blocks until the request is canceled, just like an upstream
// keeping its HTTP response open after the terminal event.
type terminalOpenBody struct {
	ctx        context.Context
	remaining  []byte
	secondRead chan struct{}
	closed     chan struct{}
}

func (b *terminalOpenBody) Read(p []byte) (int, error) {
	if len(b.remaining) > 0 {
		n := copy(p, b.remaining)
		b.remaining = b.remaining[n:]
		return n, nil
	}
	select {
	case <-b.secondRead:
	default:
		close(b.secondRead)
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *terminalOpenBody) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

var _ io.ReadCloser = (*terminalOpenBody)(nil)

func TestTurnReturnsAtTerminalEventWithBodyStillOpen(t *testing.T) {
	for _, kind := range []string{"response.completed", "response.incomplete", "response.done"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			created := `{"type":"response.created","response":{"id":"resp_terminal"}}`
			terminal := `{"type":"` + kind + `","response":{"id":"resp_terminal"}}`
			body := &terminalOpenBody{
				remaining:  []byte("data: " + created + "\n\ndata: " + terminal + "\n\n"),
				secondRead: make(chan struct{}),
				closed:     make(chan struct{}),
			}
			returned := make(chan struct{})
			readPastTerminal := make(chan bool)
			// Cancel only if Run asks for more bytes. This releases the old
			// implementation deterministically, rather than hanging the suite
			// or relying on a deadline to distinguish success from failure.
			go func() {
				select {
				case <-body.secondRead:
					cancel()
					readPastTerminal <- true
				case <-returned:
					readPastTerminal <- false
				}
			}()
			attempts := 0
			turn := &Turn{
				Request: &Request{URL: "http://upstream.invalid/responses", Header: http.Header{}},
				Do: func(request *http.Request) (*http.Response, error) {
					attempts++
					body.ctx = request.Context()
					return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
				},
			}
			var events []string
			err := turn.Run(ctx, func(event []byte) error {
				select {
				case <-body.closed:
					t.Error("response body closed before terminal events were emitted")
				default:
				}
				events = append(events, string(event))
				return nil
			})
			close(returned)
			if <-readPastTerminal {
				t.Error("Run read beyond the terminal event instead of returning while the upstream body remained open")
			}
			if err != nil {
				t.Errorf("Run returned %v, want successful terminal completion without cancellation", err)
			}
			if attempts != 1 {
				t.Errorf("HTTP attempts = %d, want 1", attempts)
			}
			if len(events) != 2 {
				t.Errorf("events = %q, want created and terminal events", events)
			} else if events[0] != created || events[1] != terminal {
				t.Errorf("events = %q, want [%s %s]", events, created, terminal)
			}
			select {
			case <-body.closed:
			default:
				t.Error("Run returned without closing the response body")
			}
		})
	}
}
