package domain

import "time"

const ThreadPeerMessageVersion = "thread_peer_message.v1"
const ThreadPeerTargetsVersion = "thread_peer_targets.v1"
const MaxThreadPeerReplyDepth = 8

type SendThreadPeerMessageRequest struct {
	SourceThreadID        string
	TargetThreadID        string
	Content               string
	ReplyTo               string
	OperationKey          string
	RequestedBy           string
	ModelInitiated        bool
	SourceRunID           string
	SourceAttemptID       string
	SourceLeaseID         string
	SourceLeaseGeneration int64
}

// Delivered records model input delivery, never completion of the requested work.
type ThreadPeerMessage struct {
	ID                string     `json:"id"`
	SourceThreadID    string     `json:"source_thread_id"`
	SourceTitle       string     `json:"source_title"`
	TargetThreadID    string     `json:"target_thread_id"`
	TargetTitle       string     `json:"target_title"`
	Content           string     `json:"content"`
	ReplyTo           string     `json:"reply_to,omitempty"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
	DeliveredAt       *time.Time `json:"delivered_at,omitempty"`
	Error             string     `json:"error,omitempty"`
	SteeringMessageID string     `json:"-"`
	RunID             string     `json:"-"`
	Depth             int        `json:"-"`
}

type ThreadPeerMessageReceipt struct {
	Version  string            `json:"version"`
	Message  ThreadPeerMessage `json:"message"`
	Replayed bool              `json:"replayed"`
}

type ThreadPeerTarget struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	WorkspaceID string `json:"workspace_id"`
	Status      string `json:"status"`
}

type ThreadPeerTargets struct {
	Version        string             `json:"version"`
	SourceThreadID string             `json:"source_thread_id"`
	WorkspaceID    string             `json:"workspace_id"`
	Items          []ThreadPeerTarget `json:"items"`
	HasMore        bool               `json:"has_more"`
}
