package executor

import (
	"context"
	"errors"
)

// ErrNoActiveUpstreamWebsocket means response.interrupt has no live upstream socket.
var ErrNoActiveUpstreamWebsocket = errors.New("no active upstream websocket for response.interrupt")

// WebsocketInput is a frame from the single downstream reader. Err terminates
// the connection; Payload is owned by the receiver and must not be replayed.
type WebsocketInput struct {
	Payload []byte
	Err     error
}
type websocketInputKey struct{}

func WithWebsocketInput(ctx context.Context, input <-chan WebsocketInput) context.Context {
	return context.WithValue(ctx, websocketInputKey{}, input)
}
func WebsocketInputFromContext(ctx context.Context) <-chan WebsocketInput {
	if ctx == nil {
		return nil
	}
	input, _ := ctx.Value(websocketInputKey{}).(<-chan WebsocketInput)
	return input
}

// WithWebsocketInputDefer lets an executor that reads the input channel hand
// back frames it does not own; the connection loop reads them before the channel.
type websocketInputDeferKey struct{}

func WithWebsocketInputDefer(ctx context.Context, deferInput func(WebsocketInput)) context.Context {
	return context.WithValue(ctx, websocketInputDeferKey{}, deferInput)
}

// DeferWebsocketInput returns a frame to the connection loop. It reports false
// when no loop accepts deferred frames.
func DeferWebsocketInput(ctx context.Context, message WebsocketInput) bool {
	if ctx == nil {
		return false
	}
	deferInput, _ := ctx.Value(websocketInputDeferKey{}).(func(WebsocketInput))
	if deferInput == nil {
		return false
	}
	deferInput(message)
	return true
}

// WithWebsocketAuthCheck supplies the live account-state check for a bound
// connection. It may reject further frames but never select another account.
type websocketAuthCheckKey struct{}

func WithWebsocketAuthCheck(ctx context.Context, check func(string) bool) context.Context {
	return context.WithValue(ctx, websocketAuthCheckKey{}, check)
}
func WebsocketAuthEnabled(ctx context.Context, authID string) bool {
	if ctx == nil {
		return true
	}
	check, _ := ctx.Value(websocketAuthCheckKey{}).(func(string) bool)
	return check == nil || check(authID)
}
