package domain

import "testing"

func TestWorkerChildTaskAlwaysRequiresCoreSurface(t *testing.T) {
	for _, profile := range []*WorkerProfile{{}, {Tools: []string{}}, {Tools: []string{"workspace_read"}}} {
		task := ChildTask{Worker: profile, Title: "worker", Goal: "execute job", Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 64, TimeoutMillis: 1000}
		spec := ChildTaskProposalSpec{Version: ChildTaskProposalVersion, Tasks: []ChildTask{task}}
		surface, _, err := ResolveChildTaskSurface(spec)
		if err != nil || surface != ChildTaskSurfaceCore {
			t.Fatalf("worker routed to %s: %v", surface, err)
		}
		spec.Tasks[0].SurfaceHint = ChildTaskSurfaceHintReadOnlyFanout
		if _, _, err := ResolveChildTaskSurface(spec); err == nil {
			t.Fatal("worker accepted read-only fan-out hint")
		}
	}
}
