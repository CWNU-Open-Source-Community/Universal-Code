package store

import "strings"

// An interactive chat keeps its Run running between replies. Rotate an already
// Full snapshot only at the same quiescent boundary required by the Thread
// permission transaction; all other running upgrades retain the old rejection.
// Derive the existing trigger from immutable migrations, preserving its legacy
// readers, downgrade rules and created/paused transitions.
func idleThreadFullReactivationStatements(previous []migration) []string {
	const trigger = "trg_run_execution_permission_snapshot_insert"
	var definition string
	for _, step := range previous {
		for _, statement := range step.Statements {
			if strings.HasPrefix(statement, "CREATE TRIGGER "+trigger+"\n") {
				definition = statement
			}
		}
	}
	const anchor = "(run.status IN ('created', 'paused') AND NOT EXISTS ("
	if strings.Count(definition, anchor) != 1 {
		panic("idle Thread Full reactivation lost the Run permission boundary")
	}
	definition = strings.Replace(definition, anchor, `((run.status IN ('created', 'paused') OR (
									run.status = 'running' AND previous.mode = 'full' AND NEW.mode = 'full'
									AND NOT EXISTS (
										SELECT 1 FROM agent_nodes agent WHERE agent.run_id = NEW.run_id
											AND (agent.status = 'running' OR agent.active_attempt_id <> '')
									)
									AND NOT EXISTS (
										SELECT 1 FROM run_supervisor_checkpoints checkpoint
										WHERE checkpoint.run_id = NEW.run_id AND checkpoint.phase = 'turn_started'
									)
									AND NOT EXISTS (
										SELECT 1 FROM operator_steering_deliveries delivery
										WHERE delivery.run_id = NEW.run_id AND delivery.status = 'prepared'
									)
									AND NOT EXISTS (
										SELECT 1 FROM command_runtime_jobs job WHERE job.run_id = NEW.run_id
											AND job.state IN ('prepared', 'running', 'stopping')
									)
									AND NOT EXISTS (
										SELECT 1 FROM terminal_sessions terminal WHERE terminal.run_id = NEW.run_id
											AND terminal.state IN ('starting', 'running')
									)
								)) AND NOT EXISTS (`, 1)
	return []string{"DROP TRIGGER " + trigger + ";", definition}
}
