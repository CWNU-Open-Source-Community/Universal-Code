package store

var workflowCompletionNotificationStatements = []string{
	`CREATE TABLE workflow_completion_notifications (
     execution_id TEXT PRIMARY KEY REFERENCES workflow_executions(id),
     status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered','cancelled')),
     reason TEXT NOT NULL DEFAULT '', next_attempt_at TEXT NOT NULL,
     wake_generation INTEGER NOT NULL DEFAULT 0 CHECK(wake_generation>=0),
     claim_owner TEXT NOT NULL DEFAULT '', claim_until TEXT NOT NULL DEFAULT '');`,
	`CREATE INDEX workflow_completion_notification_due ON workflow_completion_notifications(status,next_attempt_at,execution_id);`,
	// Reconcile historical completions through their original summary operation
	// and wake keys. Inbox commitment precedes the model response, so it alone
	// cannot establish delivery. Completed handoffs replay without model calls.
	`INSERT INTO workflow_completion_notifications(execution_id,next_attempt_at)
     SELECT id,updated_at FROM workflow_executions WHERE status='completed';`,
}
