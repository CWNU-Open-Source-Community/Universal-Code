package application

import (
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
)

const (
	FileEditChangeSetProtocolVersion = "file_edit_change_set.v1"
	MaxFileEditChangeSetItems        = 100
)

type FileEditChangeSetCounts struct {
	Proposed int
	Approved int
	Applied  int
	Denied   int
	Failed   int
}

type FileEditChangeSet struct {
	RunID          string
	SessionID      string
	WorkspaceID    string
	Items          []fileedit.Preview
	Counts         FileEditChangeSetCounts
	TotalDiffBytes int
}

func BuildFileEditChangeSet(run domain.Run, mission domain.Mission,
	previews []fileedit.Preview,
) (FileEditChangeSet, error) {
	return BuildFileEditChangeSetForWorkspace(run, mission, mission.WorkspaceID, previews)
}

// BuildFileEditChangeSetForWorkspace projects one already-resolved target. It
// does not rewrite the Mission's source identity or merge another target's history.
func BuildFileEditChangeSetForWorkspace(run domain.Run, mission domain.Mission,
	workspaceID string, previews []fileedit.Preview,
) (FileEditChangeSet, error) {
	if !validControlIdentity(run.ID) || !validControlIdentity(run.SessionID) ||
		!validControlIdentity(mission.WorkspaceID) || !validControlIdentity(workspaceID) || run.MissionID != mission.ID ||
		len(previews) > MaxFileEditChangeSetItems {
		return FileEditChangeSet{}, apperror.New(apperror.CodeInternal,
			"file edit change set binding is invalid")
	}
	result := FileEditChangeSet{RunID: run.ID, SessionID: run.SessionID,
		WorkspaceID: workspaceID, Items: append([]fileedit.Preview{}, previews...)}
	for _, preview := range previews {
		if !validControlIdentity(preview.ID) || preview.SessionID != run.SessionID ||
			preview.WorkspaceID != workspaceID ||
			preview.Path == "" ||
			!fileedit.ValidStatus(preview.Status) || len([]byte(preview.Diff)) > fileedit.MaxDiffBytes {
			return FileEditChangeSet{}, apperror.New(apperror.CodeInternal,
				"file edit change set contains a mismatched record")
		}
		result.TotalDiffBytes += len([]byte(preview.Diff))
		switch preview.Status {
		case fileedit.StatusProposed:
			result.Counts.Proposed++
		case fileedit.StatusApproved:
			result.Counts.Approved++
		case fileedit.StatusApplied:
			result.Counts.Applied++
		case fileedit.StatusDenied:
			result.Counts.Denied++
		case fileedit.StatusFailed:
			result.Counts.Failed++
		}
	}
	return result, nil
}
