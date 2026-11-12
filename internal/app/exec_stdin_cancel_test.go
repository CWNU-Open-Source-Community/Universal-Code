package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestExecStdinCancellationUnblocksWithoutInput(t *testing.T) {
	a, _, _ := newExecTestApp(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	previous := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = previous }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() { done <- a.execCommand(ctx, []string{"--workspace", "exec-ws", "--stdin"}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stdin cancellation returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled exec stayed blocked waiting for stdin")
	}
}
