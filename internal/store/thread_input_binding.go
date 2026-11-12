package store

// The first parameter scopes all claims to one Thread. A tool-boundary receipt
// binds an already saved user row while its steering input is still pending;
// the final segment commits that same row later. Keep this read-only relation
// shared by transcript ordering and input statistics instead of guessing from
// equal text or treating the steering status as an execution-completion fact.
const threadOperatorMessageBindingsCTE = `WITH thread_operator_inputs AS (
	SELECT steering.* FROM thread_runs binding
	JOIN operator_steering_messages steering ON steering.run_id=binding.run_id
		AND steering.session_id=binding.session_id
	WHERE binding.thread_id=?
), operator_message_candidates AS (
	SELECT steering.id AS operator_message_id,steering.session_message_id AS message_id,
		NULL AS boundary_sequence
	FROM thread_operator_inputs steering WHERE steering.status='committed'
	UNION ALL
	SELECT steering.id,json_extract(boundary.payload_json,'$.user_message_id'),boundary.sequence
	FROM thread_operator_inputs steering
	JOIN run_events boundary ON boundary.run_id=steering.run_id
		AND boundary.type='agent.turn_completed' AND boundary.source='run_supervisor'
		AND json_extract(boundary.payload_json,'$.tool_round_boundary')=1
		AND json_extract(boundary.payload_json,'$.lifecycle_action')='continue'
		AND json_extract(boundary.payload_json,'$.operator_message_id')=steering.id
		AND json_type(boundary.payload_json,'$.user_message_id')='integer'
	JOIN operator_steering_deliveries delivery ON delivery.message_id=steering.id
		AND delivery.run_id=steering.run_id AND delivery.attempt_id=boundary.subject_id
		AND delivery.turn=json_extract(boundary.payload_json,'$.turn') AND delivery.status='superseded'
	WHERE steering.status='pending' AND steering.session_message_id IS NULL
), bound_operator_messages AS (
	SELECT steering.id AS operator_message_id,message.id AS session_message_id,
		queued.event_id AS queued_event_id,recorded.event_id AS session_event_id
	FROM operator_message_candidates candidate
	JOIN thread_operator_inputs steering ON steering.id=candidate.operator_message_id
	JOIN session_messages message ON message.id=candidate.message_id
		AND message.session_id=steering.session_id AND message.role='user'
		AND message.source_kind='operator_message' AND message.instruction_authorized=1
		AND message.source_ref=''
		AND message.content_sha256=steering.content_sha256 AND message.content=steering.content
	JOIN run_events queued ON queued.run_id=steering.run_id
		AND queued.type='operator.steering_queued' AND queued.subject_id=steering.id
	JOIN run_events recorded ON recorded.run_id=steering.run_id
		AND recorded.type='session.message_created' AND recorded.source='session_store'
		AND recorded.subject_id=CAST(message.id AS TEXT) AND recorded.sequence>queued.sequence
		AND json_extract(recorded.payload_json,'$.message_id')=message.id
		AND json_extract(recorded.payload_json,'$.session_id')=message.session_id
		AND json_extract(recorded.payload_json,'$.role')='user'
		AND json_extract(recorded.payload_json,'$.provenance_version')=message.provenance_version
		AND json_extract(recorded.payload_json,'$.source_kind')='operator_message'
		AND COALESCE(json_extract(recorded.payload_json,'$.source_ref'),'')=message.source_ref
		AND json_extract(recorded.payload_json,'$.instruction_authorized')=1
		AND json_extract(recorded.payload_json,'$.content_sha256')=message.content_sha256
		AND json_extract(recorded.payload_json,'$.content')=message.content
		AND (candidate.boundary_sequence IS NULL OR candidate.boundary_sequence>recorded.sequence)
)`
