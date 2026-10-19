package domain

import "time"

// ThreadSearchMatch is a saved title or public message, ordered by its durable
// creation time and identity. Content and source positions stay inside Go.
type ThreadSearchMatch struct {
	ID           string
	ThreadID     string
	ThreadTitle  string
	ThreadStatus ThreadStatus
	WorkspaceID  string
	MessageID    string
	Role         string
	Content      string
	CreatedAt    time.Time
	RunOrdinal   int64
	Sequence     int64
}
