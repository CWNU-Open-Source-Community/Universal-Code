package store

import (
	"context"

	"cyberagent-workbench/internal/session"
)

// A legacy queued attachment could already have a Session evidence row before
// its operator message was delivered. The v168 receipt backfill binds that row
// to its exact owner. Pending and cancelled owners remain available to audit
// reads. A successor can publish its own exact evidence receipt for a retained,
// unexecuted input without changing the old queue entry's cancelled status.
const supervisorModelVisibleAttachmentEvidenceSQL = ` AND NOT (
	message.source_kind IN ('workspace_image','uploaded_file')
	AND instr(message.content,'operator_message_id')>0
	AND NOT EXISTS (
		SELECT 1 FROM operator_message_attachment_evidence receipt
		JOIN operator_steering_messages steering ON steering.id=receipt.message_id
		WHERE receipt.session_message_id=message.id
			AND instr(message.content,'"operator_message_id":"'||steering.id||'"')>0
			AND steering.session_id=message.session_id AND steering.status='committed'
			AND receipt.attachment_id=message.source_ref
			AND receipt.kind=CASE message.source_kind
				WHEN 'workspace_image' THEN 'image' ELSE 'file' END
	)
	AND NOT EXISTS (
		SELECT 1 FROM run_events receipt
		JOIN thread_runs retained ON retained.run_id=receipt.run_id AND retained.session_id=message.session_id
		JOIN operator_steering_messages steering ON steering.id=receipt.subject_id
			AND steering.run_id=retained.predecessor_run_id AND steering.status='cancelled'
		WHERE receipt.source='thread_epoch_continuation' AND receipt.type='session.message_created'
			AND message.id IN (SELECT value FROM json_each(receipt.payload_json,'$.attachment_evidence_ids'))
			AND instr(message.content,'"operator_message_id":"'||steering.id||'"')>0
			AND ((message.source_kind='workspace_image' AND EXISTS (
				SELECT 1 FROM thread_message_images b WHERE b.message_id=steering.id AND b.image_id=message.source_ref))
			OR (message.source_kind='uploaded_file' AND EXISTS (
				SELECT 1 FROM thread_message_attachments b WHERE b.message_id=steering.id AND b.attachment_id=message.source_ref)))
	)
)`

func listSupervisorContextMessages(ctx context.Context, reader supervisorCompactionReader,
	sessionID string,
) ([]session.Message, error) {
	rows, err := reader.QueryContext(ctx, `SELECT id, session_id, role, content, provenance_version,
		source_kind, source_ref, content_sha256, instruction_authorized, token_estimate,
		compacted, created_at FROM session_messages message
		WHERE session_id=? AND compacted=0`+supervisorModelVisibleAttachmentEvidenceSQL+` ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []session.Message{}
	for rows.Next() {
		message, err := scanSessionMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}
