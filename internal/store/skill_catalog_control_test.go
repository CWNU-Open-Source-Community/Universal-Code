package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
)

func TestSkillCatalogPublisherCASRejectsReplaysAndCLIRevocation(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "catalog-cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	publisher := SkillCatalogPublisher{Fingerprint: strings.Repeat("a", 64), Name: "Publisher", PublicKey: strings.Repeat("b", 44)}
	initial, err := st.CompareAndSetSkillCatalogPublisher(ctx, publisher, "trust", "", "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompareAndSetSkillCatalogPublisher(ctx, publisher, "trust", "", "web"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("creation replay=%v", err)
	}
	if err := st.RevokeSkillCatalogPublisher(ctx, publisher.Fingerprint, "cli"); err != nil {
		t.Fatal(err)
	}
	revoked, _, err := st.GetSkillCatalogPublisher(ctx, publisher.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.TrustClass != "revoked" || !revoked.UpdatedAt.After(initial.UpdatedAt) {
		t.Fatalf("revocation=%+v", revoked)
	}
	if _, err := st.CompareAndSetSkillCatalogPublisher(ctx, publisher, "trust", ts(initial.UpdatedAt), "web"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale page revived revoked publisher: %v", err)
	}
	restored, err := st.CompareAndSetSkillCatalogPublisher(ctx, publisher, "trust", ts(revoked.UpdatedAt), "web")
	if err != nil {
		t.Fatal(err)
	}
	if restored.TrustClass != "trusted" || restored.RevokedAt != nil || !restored.CreatedAt.Equal(initial.CreatedAt) {
		t.Fatalf("explicit regrant=%+v", restored)
	}
	if _, err := st.CompareAndSetSkillCatalogPublisher(ctx, publisher, "revoke", ts(revoked.UpdatedAt), "web"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale revoke=%v", err)
	}
	audit, err := st.ListSkillCatalogAudit(ctx, 20)
	if err != nil || len(audit) != 3 {
		t.Fatalf("failed CAS appended audit: %+v %v", audit, err)
	}
	// A future stored timestamp models a backwards clock adjustment. CLI and
	// HTTP decisions must continue advancing the observed version.
	future := time.Now().UTC().Add(time.Hour)
	if _, err := st.db.ExecContext(ctx, `UPDATE skill_catalog_publishers SET updated_at=? WHERE fingerprint=?`, ts(future), publisher.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeSkillCatalogPublisher(ctx, publisher.Fingerprint, "cli"); err != nil {
		t.Fatal(err)
	}
	latest, _, _ := st.GetSkillCatalogPublisher(ctx, publisher.Fingerprint)
	if !latest.UpdatedAt.After(future) {
		t.Fatalf("CLI reused observed version: %s", latest.UpdatedAt)
	}
}

func TestSkillCatalogPublisherCASSerializesCompetingConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog-race.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	publisher := SkillCatalogPublisher{Fingerprint: strings.Repeat("c", 64), Name: "Concurrent", PublicKey: strings.Repeat("d", 44)}
	initial, err := first.CompareAndSetSkillCatalogPublisher(t.Context(), publisher, "trust", "", "setup")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, st := range []*SQLiteStore{first, second} {
		wg.Add(1)
		go func(st *SQLiteStore) {
			defer wg.Done()
			<-start
			_, err := st.CompareAndSetSkillCatalogPublisher(context.Background(), publisher, "revoke", ts(initial.UpdatedAt), "web")
			results <- err
		}(st)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if apperror.CodeOf(err) == apperror.CodeConflict {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	audit, err := first.ListSkillCatalogAudit(t.Context(), 20)
	if err != nil || len(audit) != 2 {
		t.Fatalf("competing audit=%+v %v", audit, err)
	}
}
