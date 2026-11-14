package githubreview

import (
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/gitadvanced"
)

func TestBuildEvidenceGraphMapsStableHunkAndMarksHeadDriftStale(t *testing.T) {
	const exactPath = " file.go "
	now := time.Date(2026, 8, 21, 2, 0, 0, 0, time.UTC)
	base := strings.Repeat("1", 40)
	head := strings.Repeat("2", 40)
	merge := strings.Repeat("3", 40)
	repo, _ := ParseRepository("acme/widget")
	ref := CredentialReference{Name: "github-review", Kind: AuthFineGrainedPAT}
	capability := testCapability(repo, ref, now, false)
	snapshot := Snapshot{ProtocolVersion: SnapshotProtocolVersion,
		Identity: PullRequestIdentity{Repository: repo, Number: 7, NodeID: "PR_7",
			State: "open", BaseRef: "main", BaseSHA: base, HeadRef: "feature",
			HeadSHA: head, MergeBaseSHA: merge, UpdatedAt: now},
		Capability: capability, Title: SanitizeRemoteText("title", MaxTextBytes),
		Body: EmptyTextEvidence(), Author: "octocat", RequestedReviewers: []string{},
		Files: []ChangedFile{}, Reviews: []Review{},
		Threads: []ReviewThread{{ID: "thread_1", Path: sanitizeRepositoryPath(exactPath), Side: "RIGHT", Line: 3,
			Comments: []Comment{{NodeID: "comment_1", Author: "reviewer",
				Body:      SanitizeRemoteText("fix", MaxTextBytes),
				Position:  Position{Path: sanitizeRepositoryPath(exactPath), Side: "RIGHT", Line: 3, CommitSHA: head},
				CreatedAt: now, UpdatedAt: now}}}},
		LooseComments: []Comment{}, CheckSuites: []CheckSuite{}, CheckRuns: []CheckRun{},
		Jobs: []WorkflowJob{}, Artifacts: []ArtifactMetadata{}, Pagination: []PageEvidence{},
		State: EvidenceVerified, Omissions: []string{}, FetchedAt: now}
	snapshot.Finalize()
	digest := strings.Repeat("a", 64)
	diff := gitadvanced.ReviewDiffEvidence{ProtocolVersion: gitadvanced.ReviewDiffProtocolVersion,
		Binding: gitadvanced.RepositoryBinding{ProtocolVersion: gitadvanced.ProtocolVersion,
			RepositorySHA256: digest, CommonDirSHA256: digest, Head: head, Branch: "feature",
			IndexSHA256: digest, WorktreeSHA256: digest, StatusSHA256: digest,
			StashSHA256: digest, SequenceSHA256: digest, ObjectFormat: "sha1", CapturedAt: now},
		BaseSHA: base, HeadSHA: head, MergeBaseSHA: merge, DiffSHA256: digest,
		CallChainSHA256: digest, DiffBytes: 100, DiffStat: "1 file changed",
		ChangedFiles: []string{exactPath}, Hunks: []gitadvanced.Hunk{{ID: digest,
			Path: exactPath, OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 4,
			BaseBlob: base, IndexBlob: head, WorktreeSHA256: digest,
			ContextSHA256: digest, PatchSHA256: digest, Patch: "@@ -1,3 +1,4 @@\n"}},
		Conflict: gitadvanced.ConflictState{Files: []gitadvanced.ConflictFile{}},
		Complete: true, Omissions: []string{}, CapturedAt: now}
	graph, err := BuildEvidenceGraph(snapshot, diff, nil, now.Add(time.Second))
	if err != nil || graph.State != EvidenceVerified || len(graph.Mappings) != 1 ||
		graph.Mappings[0].Path != exactPath || graph.Mappings[0].HunkID != digest ||
		graph.Mappings[0].Semantic.State != EvidenceNotRun {
		t.Fatalf("current evidence graph failed: %v %#v", err, graph)
	}
	staleDiff := diff
	staleDiff.Binding.Head = strings.Repeat("4", 40)
	staleDiff.HeadSHA = staleDiff.Binding.Head
	stale, err := BuildEvidenceGraph(snapshot, staleDiff, nil, now.Add(2*time.Second))
	if err != nil || stale.State != EvidenceStale || stale.Mappings[0].State != EvidenceStale ||
		!strings.Contains(strings.Join(stale.Omissions, " "), "head commit drifted") {
		t.Fatalf("head drift was not marked stale: %v %#v", err, stale)
	}
}
