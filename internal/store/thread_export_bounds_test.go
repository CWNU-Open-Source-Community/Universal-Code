package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

func TestThreadExportBoundsBulkSectionsAndReportsTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export-bounds.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	workspace := WorkspaceRecord{ID: "workspace-export", Name: "export bounds",
		RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err = st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	created, err := application.NewControlledRunCreationService(st).Create(t.Context(),
		application.ControlledRunCreationRequest{Version: domain.RunCreationProtocolVersion,
			Goal: "export bounds fixture", WorkspaceID: workspace.ID,
			OperationKey: "export-bounds-original-01", RequestedBy: "http_thread_operator"})
	if err != nil {
		t.Fatal(err)
	}
	runService := application.NewRunService(st)
	if _, err = runService.Start(t.Context(), created.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runService.Fail(t.Context(), created.Run.ID, "fixture later failure"); err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(created.Run.ID)
	bindings, err := st.ListThreadRuns(t.Context(), threadID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("thread binding missing: %v", err)
	}
	sessionID := bindings[0].SessionID
	now := ts(time.Now().UTC())

	// One message beyond the section cap. The rows satisfy the provenance
	// trigger's operator_message branch.
	for i := 0; i < maxThreadExportSection+1; i++ {
		content := fmt.Sprintf("message %04d", i)
		digest := sha256.Sum256([]byte(content))
		if _, err = st.db.Exec(`INSERT INTO session_messages
			(session_id, role, content, token_estimate, compacted, created_at,
			 provenance_version, source_kind, source_ref, content_sha256, instruction_authorized)
			VALUES (?, 'user', ?, 1, 0, ?, ?, 'operator_message', '', ?, 1)`, sessionID,
			content, now, session.ContextProvenanceVersion, hex.EncodeToString(digest[:])); err != nil {
			t.Fatal(err)
		}
	}
	// Five events and audit events beyond the section cap. The audit rows
	// continue the run's existing event sequence (lifecycle events already
	// occupy the low sequences).
	var sequenceBase int
	if err = st.db.QueryRow(`SELECT COALESCE(MAX(sequence), 0) FROM run_events WHERE run_id = ?`,
		created.Run.ID).Scan(&sequenceBase); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxThreadExportSection+5; i++ {
		if _, err = st.db.Exec(`INSERT INTO thread_events
			(thread_id, run_id, type, source, payload_json, created_at)
			VALUES (?, ?, 'fixture', 'test', '{}', ?)`, threadID, created.Run.ID, now); err != nil {
			t.Fatal(err)
		}
		if _, err = st.db.Exec(`INSERT INTO run_events
			(event_id, version, run_id, mission_id, sequence, type, source, subject_id, payload_json, created_at)
			VALUES (?, 'v1', ?, ?, ?, 'fixture', 'test', '', '{}', ?)`,
			fmt.Sprintf("export-bounds-%04d", i), created.Run.ID, created.Run.MissionID,
			sequenceBase+i+1, now); err != nil {
			t.Fatal(err)
		}
	}
	exported, err := st.ExportThread(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Messages) != maxThreadExportSection || !exported.MessagesTruncated {
		t.Fatalf("message section not bounded: %d truncated=%t", len(exported.Messages), exported.MessagesTruncated)
	}
	if len(exported.Events) != maxThreadExportSection || !exported.EventsTruncated {
		t.Fatalf("event section not bounded: %d truncated=%t", len(exported.Events), exported.EventsTruncated)
	}
	if len(exported.AuditEvents) != maxThreadExportSection || !exported.AuditEventsTruncated {
		t.Fatalf("audit section not bounded: %d truncated=%t", len(exported.AuditEvents), exported.AuditEventsTruncated)
	}
	// Sections stay chronological prefixes of their ledgers.
	if !strings.Contains(exported.Messages[0].Content, "message 0000") {
		t.Fatalf("message section lost its oldest row: %q", exported.Messages[0].Content)
	}
	if exported.AuditEvents[0].Sequence > exported.AuditEvents[len(exported.AuditEvents)-1].Sequence {
		t.Fatalf("audit section ordering broke: %+v .. %+v", exported.AuditEvents[0],
			exported.AuditEvents[len(exported.AuditEvents)-1])
	}
	if exported.Events[0].Type != "thread.created" ||
		exported.Events[len(exported.Events)-1].Type != "fixture" {
		t.Fatalf("event section lost its chronological prefix: first=%q last=%q",
			exported.Events[0].Type, exported.Events[len(exported.Events)-1].Type)
	}

	// A Thread whose ledgers fit under the caps exports whole and unflagged.
	small, err := application.NewControlledRunCreationService(st).Create(t.Context(),
		application.ControlledRunCreationRequest{Version: domain.RunCreationProtocolVersion,
			Goal: "export bounds small", WorkspaceID: workspace.ID,
			OperationKey: "export-bounds-successor-02", RequestedBy: "http_thread_operator"})
	if err != nil {
		t.Fatal(err)
	}
	smallExport, err := st.ExportThread(t.Context(), domain.InitialThreadID(small.Run.ID))
	if err != nil {
		t.Fatal(err)
	}
	if smallExport.MessagesTruncated || smallExport.EventsTruncated || smallExport.AuditEventsTruncated {
		t.Fatalf("small Thread reported truncation: %+v", smallExport)
	}
	// A fresh Thread carries only its lifecycle rows; the message ledger is
	// still empty.
	if len(smallExport.Messages) != 0 {
		t.Fatalf("small Thread gained messages: %d", len(smallExport.Messages))
	}
	if len(smallExport.Events) > maxThreadExportSection || len(smallExport.AuditEvents) > maxThreadExportSection {
		t.Fatalf("small Thread sections exceeded the cap: %d/%d", len(smallExport.Events),
			len(smallExport.AuditEvents))
	}
}
