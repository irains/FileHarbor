package utils

import (
	"context"
	"os"
)

// OperationEvent reports measured work and publication boundaries. Observers
// may reject a transition (for example when its durable journal cannot sync).
// They receive relative paths only and must not reenter the operation gate.
type OperationEvent struct {
	Phase string `json:"phase"`
	Bytes int64  `json:"bytes,omitempty"`
	Items int64  `json:"items,omitempty"`
	Path  string `json:"path,omitempty"`
}

type OperationObserver func(OperationEvent) error
type operationObserverKey struct{}

func WithOperationObserver(ctx context.Context, observer OperationObserver) context.Context {
	return context.WithValue(ctx, operationObserverKey{}, observer)
}

func observeOperation(ctx context.Context, event OperationEvent) error {
	if observer, ok := ctx.Value(operationObserverKey{}).(OperationObserver); ok && observer != nil {
		return observer(event)
	}
	return nil
}

func hasOperationObserver(ctx context.Context) bool {
	observer, ok := ctx.Value(operationObserverKey{}).(OperationObserver)
	return ok && observer != nil
}

// cleanupOperationStage only removes the same directory created by this call.
// A replaced or unresolved stage is preserved for manual investigation.
func cleanupOperationStage(ctx context.Context, stage, relative string, expected os.FileInfo) {
	current, err := os.Lstat(stage)
	if os.IsNotExist(err) {
		_ = observeOperation(ctx, OperationEvent{Phase: "stage_cleaned", Path: relative})
		return
	}
	if err != nil || expected == nil || !os.SameFile(current, expected) || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return
	}
	if err := os.RemoveAll(stage); err == nil {
		_ = observeOperation(ctx, OperationEvent{Phase: "stage_cleaned", Path: relative})
	}
}
