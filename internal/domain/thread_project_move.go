package domain

const ThreadProjectMoveVersion = "thread_project_move.v1"

type ThreadProjectMoveRequest struct {
	Version            string
	ThreadID           string
	ProjectWorkspaceID string
	ExpectedVersion    int64
	OperationKey       string
	RequestedBy        string
}
type ThreadProjectMoveResult struct {
	Thread   Thread `json:"thread"`
	Replayed bool   `json:"replayed"`
}
