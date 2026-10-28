package usage

import "context"

type observerContextKey struct{}

// WithObserver attaches an in-process accounting observer to this execution.
// Publish invokes it synchronously before asynchronous sink delivery, so final
// settlement never races the usage queue. No observer is installed by default.
func WithObserver(ctx context.Context, observer func(Record)) context.Context {
	return context.WithValue(ctx, observerContextKey{}, observer)
}

func observe(ctx context.Context, record Record) {
	if ctx == nil {
		return
	}
	if observer, ok := ctx.Value(observerContextKey{}).(func(Record)); ok && observer != nil {
		observer(record)
	}
}
