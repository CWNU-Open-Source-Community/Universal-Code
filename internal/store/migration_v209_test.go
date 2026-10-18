package store

import (
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
)

func TestSchemaV209PreservesObservationConsentRoundsAndForeignKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduled-v208.db")
	st := openHistoricalTestDatabase(t, path, 208)
	run := createV167ScheduledRun(t, st)
	now := time.Now().UTC().Add(time.Second).Truncate(time.Millisecond)
	req := v167CreateRequest(run.ID, "v209-old-observation", now)
	req.ObservationConsentVersion = 1
	created, err := application.NewScheduledJobService(st).WithClock(&v167Clock{now: now}).Create(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	digest := runmutation.Fingerprint("v209-round")
	_, err = st.db.ExecContext(t.Context(), `INSERT INTO scheduled_job_rounds(job_id,occurrence_at,protocol_version,ordinal,attempt,claim_generation,fence_token_sha256,owner_id_sha256,status,event_sequence,observation_sha256,changed,model_called,tool_called,result,error_code,started_at,completed_at) VALUES(?,?,'scheduled-job-round.v1',1,1,1,?,?,'completed',0,?,1,0,0,'existing observation','',?,?)`, created.Job.ID, ts(req.Schedule.AnchorAt), digest, digest, digest, ts(now), ts(now))
	if err != nil {
		t.Fatal(err)
	}
	next := req.Schedule.AnchorAt.Add(time.Minute)
	if _, err := st.db.ExecContext(t.Context(), `UPDATE scheduled_jobs SET rounds_completed=1,last_result='existing observation',last_observation_sha256=?,next_wake_at=? WHERE id=?`, digest, ts(next), created.Job.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := st.db.QueryContext(t.Context(), `SELECT type,name,sql FROM sqlite_master WHERE name LIKE '%scheduled_job%' AND name<>'scheduled_jobs' AND type IN('table','trigger','index') AND sql IS NOT NULL ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]string{}
	for rows.Next() {
		var kind, name, statement string
		if err := rows.Scan(&kind, &name, &statement); err != nil {
			t.Fatal(err)
		}
		original[name] = statement
	}
	rows.Close()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	job, err := upgraded.GetScheduledJob(t.Context(), created.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ObservationConsentVersion != 1 || job.ExecutionConsentVersion != 0 || job.RoundsCompleted != 1 || job.LastResult != "existing observation" {
		t.Fatalf("job=%+v", job)
	}
	rounds, err := upgraded.ListScheduledJobRounds(t.Context(), job.ID, 10)
	if err != nil || len(rounds) != 1 || rounds[0].Result != "existing observation" {
		t.Fatalf("rounds=%+v err=%v", rounds, err)
	}
	for name, want := range original {
		var got string
		if err := upgraded.db.QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&got); err != nil || got != want {
			t.Fatalf("schema object %s changed: %q != %q err=%v", name, got, want, err)
		}
	}
	for _, statement := range []string{`UPDATE scheduled_job_observation_consents SET version=1`, `DELETE FROM scheduled_job_observation_consents`} {
		if _, err := upgraded.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatalf("consent protection lost: %s", statement)
		}
	}
	var executionConsents int
	if err := upgraded.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM scheduled_job_execution_consents`).Scan(&executionConsents); err != nil || executionConsents != 0 {
		t.Fatalf("execution consent backfilled=%d err=%v", executionConsents, err)
	}
	assertNoForeignKeyViolations(t, upgraded.db)
	for pragma, want := range map[string]int{"foreign_keys": 1, "legacy_alter_table": 0} {
		var got int
		if err := upgraded.db.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("%s=%d err=%v", pragma, got, err)
		}
	}
	claimed, _, found, err := upgraded.ClaimDueConfirmedScheduledJob(t.Context(), "v209-observer", next)
	if err != nil || !found || claimed.ID != job.ID || claimed.Spec.ExecutionMode != domain.ScheduledJobReadOnly {
		t.Fatalf("claim=%+v found=%v err=%v", claimed, found, err)
	}
}
