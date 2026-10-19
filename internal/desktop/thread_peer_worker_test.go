package desktop

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/credential"
)

func TestControlPlaneOwnsPeerDeliveryWorkerLifetime(t *testing.T) {
	plane, err := OpenControlPlane(ControlPlaneConfig{
		DatabasePath: filepath.Join(t.TempDir(), "peer-lifetime.db"),
		ReadToken:    desktopControlPlaneTestToken, ControlToken: desktopControlPlaneControlToken,
		RunExecutionEnabled: true, ModelControlEnabled: true,
		CredentialStore: credential.NewMemoryStore(), AppVersion: "peer-lifetime-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	if plane.threadPeerMessages == nil {
		t.Fatal("desktop omitted the peer messaging service")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := plane.StartWakeWorker(ctx); err != nil {
		t.Fatal(err)
	}
	if plane.threadPeerDone == nil {
		t.Fatal("desktop did not start peer delivery")
	}
	if err := plane.StartWakeWorker(ctx); err == nil {
		t.Fatal("desktop started a second peer worker")
	}
	done := plane.threadPeerDone
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("peer worker outlived its desktop context")
	}
	if err := plane.Close(); err != nil {
		t.Fatal(err)
	}
	if plane.threadPeerDone != nil || plane.threadPeerCancel != nil {
		t.Fatal("desktop did not release peer worker lifecycle")
	}
}
