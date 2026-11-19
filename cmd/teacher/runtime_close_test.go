package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTeacherRuntimeCloseErrorsAreJoined(t *testing.T) {
	first := errors.New("media close")
	second := errors.New("transport close")
	joined := joinRuntimeCloseErrors(first, nil, second)
	if !errors.Is(joined, first) || !errors.Is(joined, second) {
		t.Fatalf("joined error does not preserve both causes: %v", joined)
	}
}

func TestTeacherRuntimeLifecycleRejectsTasksAfterClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &Runtime{lifecycleCtx: ctx, lifecycleStop: cancel}
	rt.lifecycleMu.Lock()
	rt.lifecycleClosed = true
	rt.lifecycleMu.Unlock()
	started := rt.goLifecycle(func(context.Context) { t.Error("closed runtime started a task") })
	if started {
		t.Fatal("expected lifecycle task to be rejected after close")
	}
}

func TestTeacherRuntimeLifecycleWaitsForTask(t *testing.T) {
	rt := &Runtime{}
	rt.lifecycleCtx, rt.lifecycleStop = context.WithCancel(context.Background())
	done := make(chan struct{})
	if !rt.goLifecycle(func(context.Context) {
		defer close(done)
		time.Sleep(10 * time.Millisecond)
	}) {
		t.Fatal("expected lifecycle task to start")
	}
	<-done
	rt.lifecycleStop()
	rt.lifecycleWG.Wait()
}
