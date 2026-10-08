package worker

import (
	"context"
	"testing"
	"time"
)

type expirer struct{ called chan struct{} }

func (e expirer) Expire(context.Context) (int64, error) {
	select {
	case e.called <- struct{}{}:
	default:
	}
	return 1, nil
}

func TestWorkerStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { Run(ctx, expirer{called}, time.Millisecond); close(done) }()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("обработчик не запустился")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("goroutine не остановилась")
	}
}
