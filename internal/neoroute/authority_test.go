package neoroute

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
)

func TestQueuedSuspensionReturnsCurrent403(t *testing.T) {
	f := newRouteFixture(t, "fill")
	f.vm.Stages[0].Cap = 0
	f.vm.Terminal.Kind = "queue"
	f.save(t)
	entered := make(chan struct{})
	f.route.pools = &observedPool{Pool: f.pool, entered: entered}
	done := make(chan error, 1)
	go func() { _, err := f.route.Run(f.ctx, boundedRequest(false), successful); done <- err }()
	<-entered
	if _, err := f.identity.UpdateUser(context.Background(), f.user.ID, neo.UserInput{Handle: f.user.Handle, Role: "service", State: "suspended", Grants: []neo.Grant{{VirtualModel: f.vm.ID, Allotment: 1}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var policy *Error
		if !errors.As(err, &policy) || policy.Status != 403 || policy.Code != "neoproxy_suspended" {
			t.Fatalf("suspended queue status: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("suspension did not wake queued inference")
	}
}
