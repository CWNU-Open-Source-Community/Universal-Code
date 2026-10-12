package store

var specialistContinuationStatements = []string{
	`ALTER TABLE specialist_schedules ADD COLUMN continuation_digest TEXT NOT NULL DEFAULT ''
		CHECK(continuation_digest = '' OR (length(continuation_digest) = 64 AND continuation_digest NOT GLOB '*[^0-9a-f]*'));`,
	`CREATE INDEX specialist_schedule_continuation ON specialist_schedules(continuation_digest, started_at DESC, id DESC)
		WHERE continuation_digest <> '';`,
	`CREATE TRIGGER specialist_schedule_continuation_immutable
		BEFORE UPDATE OF continuation_digest ON specialist_schedules
		BEGIN SELECT RAISE(ABORT, 'Specialist continuation identity is immutable'); END;`,
}
