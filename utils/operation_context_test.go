package utils

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOperationContextCancelsWhileWaiting(t *testing.T) {
	operationMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- WithOperationContext(ctx, func() error { t.Error("cancelled callback executed"); return nil })
	}()
	cancel()
	select {
	case err := <-result:
		operationMu.Unlock()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		operationMu.Unlock()
		t.Fatal("cancelled waiter did not return")
	}
	if err := WithOperationLock(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestOperationContextDoesNotRunCancelledCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WithOperationContext(ctx, func() error { t.Error("callback executed"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
