package desktop

import (
	"path/filepath"
	"testing"
	"time"
)

func TestControlPlaneWorkflowWorkerUsesAuthorizedHostLifetime(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "read-only"
		if enabled {
			name = "model-control"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			plane, err := OpenControlPlane(ControlPlaneConfig{
				DatabasePath: filepath.Join(root, "workflow-worker.db"), HomePath: root,
				ReadToken: desktopControlPlaneTestToken, ControlToken: desktopControlPlaneControlToken,
				ModelControlEnabled: enabled, AppVersion: "workflow-host-test",
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = plane.Close() })
			if err = plane.StartWakeWorker(t.Context()); err != nil {
				t.Fatal(err)
			}
			done := plane.workflowDone
			if !enabled {
				if done != nil || plane.workflowWorker != nil {
					t.Fatal("read-only host started saved workflow execution")
				}
				return
			}
			if done == nil {
				t.Fatal("workflow worker was not started with its host")
			}
			if err = plane.StartWakeWorker(t.Context()); err == nil {
				t.Fatal("a second workflow worker was started")
			}
			closed := make(chan error, 1)
			go func() { closed <- plane.Close() }()
			select {
			case err = <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("host close did not stop the workflow worker")
			}
			select {
			case <-done:
			default:
				t.Fatal("host closed its resources before the workflow worker stopped")
			}
			if err = plane.StartWakeWorker(t.Context()); err == nil {
				t.Fatal("closed host restarted its workflow worker")
			}
		})
	}
}
