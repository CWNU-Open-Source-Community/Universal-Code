package runner

import (
	"errors"
	"testing"
)

func TestCommandRuntimePreservesProgramExitAndRejectsInfrastructureExit(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"program_skip", nil, 125},
		{"transport_failure", errors.New("adapter wait failed"), -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newCommandRuntimeMemoryStore()
			starter := &commandRuntimeFakeStarter{}
			manager, err := NewCommandRuntimeManager(store, starter, "exit-evidence-owner")
			if err != nil {
				t.Fatal(err)
			}
			request := commandRuntimeTestRequest(manager, 2000)
			job, _, err := manager.Start(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			process := starter.last()
			process.mu.Lock()
			process.waitErr = test.err
			process.mu.Unlock()
			process.finish(125)
			terminal, _ := waitCommandRuntimeTerminal(t, manager, job.ID)
			if terminal.State != CommandRuntimeJobFailed || terminal.ExitCode == nil || *terminal.ExitCode != test.want {
				t.Fatalf("exit evidence lost: %+v", terminal)
			}
			stored, err := store.GetCommandRuntimeJob(t.Context(), job.ID)
			if err != nil || stored.ExitCode == nil || *stored.ExitCode != test.want {
				t.Fatalf("persisted exit: %+v %v", stored, err)
			}
			replay, replayed, err := manager.Start(t.Context(), request)
			if err != nil || !replayed || replay.ExitCode == nil || *replay.ExitCode != test.want || starter.starts != 1 {
				t.Fatalf("exit replay lost or re-executed: %+v %v", replay, err)
			}
		})
	}
}
