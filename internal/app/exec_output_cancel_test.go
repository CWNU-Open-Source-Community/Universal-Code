package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/llm"
)

func TestExecCancellationKeepsCodeWhenReplyFileFails(t *testing.T) {
	a, _, _ := newExecContinueTestApp(t)
	provider := &cancellingExecProvider{started: make(chan struct{})}
	a.router = llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	a.router.RegisterProvider(provider)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- a.execCommand(ctx, []string{
			"--workspace", "exec-ws", "--route", provider.Name() + "/model",
			"--output-last-message", filepath.Join(t.TempDir(), "missing", "reply.txt"), "Any task"})
	}()
	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("exec never reached the model")
	}
	cancel()
	select {
	case err := <-done:
		if apperror.CodeOf(err) != apperror.CodeCancelled || !strings.Contains(err.Error(), "--output-last-message") {
			t.Fatalf("reply file failure hid the cancellation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("exec did not stop after cancellation")
	}
}
