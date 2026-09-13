package utils

import "context"

// operationGate shares one mutation boundary between synchronous operations and
// background jobs. Its channel is initialized before it is shared with callers.
type operationGate struct {
	token chan struct{}
}

func newOperationGate() *operationGate {
	return &operationGate{token: make(chan struct{}, 1)}
}

func (gate *operationGate) Lock() {
	gate.token <- struct{}{}
}

func (gate *operationGate) Unlock() {
	<-gate.token
}

func (gate *operationGate) LockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case gate.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			gate.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WithOperationContext also allows cancellation while waiting for another
// mutation. Like WithOperationLock, its callback must not acquire the gate again.
func WithOperationContext(ctx context.Context, operation func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := operationMu.LockContext(ctx); err != nil {
		return err
	}
	defer operationMu.Unlock()
	return operation()
}
