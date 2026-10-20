package store

var threadPinStatements = []string{
	`ALTER TABLE threads ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0 CHECK(pinned IN (0,1));`,
	`CREATE INDEX idx_threads_status_pinned_created ON threads(status,pinned,created_at DESC,id DESC);`,
}
