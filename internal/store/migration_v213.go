package store

import "strings"

var threadPeerMessageStatements = func() []string {
	statements := []string{
		`CREATE TABLE thread_peer_messages (
		 id TEXT PRIMARY KEY, source_thread_id TEXT NOT NULL REFERENCES threads(id),
		 target_thread_id TEXT NOT NULL REFERENCES threads(id),
		 steering_message_id TEXT NOT NULL UNIQUE REFERENCES operator_steering_messages(id),
		 source_run_id TEXT NOT NULL REFERENCES runs(id),
		 operation_key_digest TEXT NOT NULL UNIQUE, request_fingerprint TEXT NOT NULL,
		 reply_to TEXT NOT NULL DEFAULT '', depth INTEGER NOT NULL CHECK(depth BETWEEN 0 AND 8),
		 delivered_at TEXT, error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
		 CHECK(source_thread_id<>target_thread_id));`,
		`CREATE INDEX idx_thread_peer_target ON thread_peer_messages(target_thread_id,created_at);`,
		`CREATE INDEX idx_thread_peer_source ON thread_peer_messages(source_thread_id,created_at);`,
		`CREATE TRIGGER trg_thread_peer_binding BEFORE INSERT ON thread_peer_messages WHEN NOT EXISTS (
		 SELECT 1 FROM threads source JOIN threads target ON target.workspace_id=source.workspace_id
		 JOIN thread_runs origin ON origin.thread_id=source.id AND origin.run_id=NEW.source_run_id
		 JOIN thread_runs recipient ON recipient.thread_id=target.id
		 JOIN operator_steering_messages message ON message.run_id=recipient.run_id
		 WHERE source.id=NEW.source_thread_id AND target.id=NEW.target_thread_id
		 AND source.status='active' AND target.status='active' AND message.id=NEW.steering_message_id
		 AND message.status='pending' AND message.delivery_mode='next_turn' AND message.image_count=0 AND message.attachment_count=0)
		 BEGIN SELECT RAISE(ABORT,'Peer message scope binding is invalid'); END;`,
		`CREATE TRIGGER trg_thread_peer_identity_immutable BEFORE UPDATE ON thread_peer_messages
		 WHEN NEW.id IS NOT OLD.id OR NEW.source_thread_id IS NOT OLD.source_thread_id OR NEW.target_thread_id IS NOT OLD.target_thread_id
		 OR NEW.steering_message_id IS NOT OLD.steering_message_id OR NEW.source_run_id IS NOT OLD.source_run_id
		 OR NEW.operation_key_digest IS NOT OLD.operation_key_digest OR NEW.request_fingerprint IS NOT OLD.request_fingerprint
		 OR NEW.reply_to IS NOT OLD.reply_to OR NEW.depth IS NOT OLD.depth OR NEW.created_at IS NOT OLD.created_at
		 OR (OLD.delivered_at IS NOT NULL AND NEW.delivered_at IS NOT OLD.delivered_at)
		 BEGIN SELECT RAISE(ABORT,'Peer message identity is immutable'); END;`,
		`DROP TRIGGER trg_operator_steering_commit_binding;`,
		`CREATE TRIGGER trg_operator_steering_commit_binding BEFORE UPDATE ON operator_steering_messages
		 WHEN NEW.status='committed' AND NOT EXISTS (SELECT 1 FROM session_messages message
		 WHERE message.id=NEW.session_message_id AND message.session_id=NEW.session_id
		 AND message.content=NEW.content AND message.content_sha256=NEW.content_sha256
		 AND ((message.role='user' AND message.source_kind='operator_message' AND message.instruction_authorized=1
		 AND NOT EXISTS(SELECT 1 FROM thread_peer_messages peer WHERE peer.steering_message_id=NEW.id))
		 OR (message.role='tool' AND message.source_kind='peer_message' AND message.instruction_authorized=0
		 AND EXISTS(SELECT 1 FROM thread_peer_messages peer WHERE peer.steering_message_id=NEW.id
		 AND message.source_ref='thread:'||peer.source_thread_id||'/peer-message:'||peer.id))))
		 BEGIN SELECT RAISE(ABORT,'operator steering Session message binding is invalid'); END;`,
	}
	for _, statement := range fileAttachmentSteeringMigration() {
		if strings.HasPrefix(strings.TrimSpace(statement), "CREATE TRIGGER trg_session_message_provenance_insert") {
			statements = append(statements, `DROP TRIGGER trg_session_message_provenance_insert;`, strings.Replace(statement, "'uploaded_file',", "'peer_message', 'uploaded_file',", 1))
		}
	}
	for _, statement := range operatorSteeringControlStatements {
		if strings.HasPrefix(strings.TrimSpace(statement), "CREATE TRIGGER trg_operator_steering_cancellation_insert") {
			statement = strings.Replace(statement, "run.status IN ('running', 'paused')", `(run.status IN ('running', 'paused') OR (run.status='created' AND NEW.requested_by='peer_delivery' AND EXISTS(SELECT 1 FROM thread_peer_messages peer WHERE peer.steering_message_id=message.id)))`, 1)
			statements = append(statements, `DROP TRIGGER trg_operator_steering_cancellation_insert;`, statement)
		}
	}
	return statements
}()
