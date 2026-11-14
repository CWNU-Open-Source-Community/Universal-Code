package store

import "strings"

// Workspace path spelling is part of the attachment and message binding.
// Copy existing rows before reinstalling the insert trigger, because their
// Runs and Sessions may have finished since the original attachment.
var exactWorkspaceEvidencePathStatements = func() []string {
	table := strings.Replace(sessionEvidenceAttachmentStatements[0],
		"CREATE TABLE session_evidence_attachments (", "CREATE TABLE session_evidence_attachments_v231 (", 1)
	table = strings.Replace(table, "CHECK(source_ref = trim(source_ref) AND length(source_ref)",
		"CHECK(length(source_ref)", 1)
	statements := []string{
		table,
		"INSERT INTO session_evidence_attachments_v231 SELECT * FROM session_evidence_attachments;",
		"DROP TABLE session_evidence_attachments;",
		"ALTER TABLE session_evidence_attachments_v231 RENAME TO session_evidence_attachments;",
	}
	statements = append(statements, sessionEvidenceAttachmentStatements[1:]...)
	for _, statement := range threadPeerMessageStatements {
		if strings.HasPrefix(strings.TrimSpace(statement), "CREATE TRIGGER trg_session_message_provenance_insert") {
			statements = append(statements,
				"DROP TRIGGER trg_session_message_provenance_insert;",
				strings.Replace(statement, "AND NEW.source_ref = trim(NEW.source_ref)",
					"AND (NEW.source_kind IN ('workspace_file', 'workspace_listing', 'workspace_diff')"+
						"\n\t\t\t\tOR NEW.source_ref = trim(NEW.source_ref))", 1))
			break
		}
	}
	return statements
}()
