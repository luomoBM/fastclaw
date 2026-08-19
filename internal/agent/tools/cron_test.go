package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestCreateCronJobPersistsMessageAccountID(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	r.SetOwnerUserID("user-1")
	r.SetChatterUserID("user-1")
	r.SetMessageContext("telegram", "dclaw_official_bot", "8169894742")
	RegisterCronTools(r, db, "user-1", "agent-1")

	args, err := json.Marshal(createCronJobArgs{
		Name:     "telegram reminder",
		Type:     "once",
		Schedule: time.Now().Add(time.Hour).Format(time.RFC3339),
		Message:  "提醒我",
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	if _, err := r.Execute(context.Background(), "create_cron_job", string(args)); err != nil {
		t.Fatalf("create cron job: %v", err)
	}

	jobs, err := db.ListCronJobsByAgent(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("list cron jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d cron jobs, want 1", len(jobs))
	}
	if got := jobs[0].AccountID; got != "dclaw_official_bot" {
		t.Fatalf("AccountID = %q, want dclaw_official_bot", got)
	}
	if got := jobs[0].Channel; got != "telegram" {
		t.Fatalf("Channel = %q, want telegram", got)
	}
	if got := jobs[0].ChatID; got != "8169894742" {
		t.Fatalf("ChatID = %q, want 8169894742", got)
	}
	if got := jobs[0].ChatterID; got != "user-1" {
		t.Fatalf("ChatterID = %q, want user-1 (stamped from registry chatter)", got)
	}
}

// TestCronJobsChatterIsolation verifies the per-chatter tenancy fix: two
// chatters sharing one public agent must only see (and delete) the jobs
// they created. This is the regression for the issue where a reminder
// created via the WeChat channel was visible to other chatters of the
// same agent.
func TestCronJobsChatterIsolation(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Two chatters share one public agent. Each is backed by its own
	// Registry instance (the per-turn identity is instance-level state)
	// but they all point at the same store + agent, so their jobs land
	// in the same cron_jobs rows — exactly the production layout.
	newRegistry := func(chatter string) *Registry {
		r := NewRegistry(t.TempDir(), t.TempDir())
		r.SetOwnerUserID("owner-1")
		r.SetChatterUserID(chatter)
		RegisterCronTools(r, db, "owner-1", "agent-1")
		return r
	}
	aliceReg := newRegistry("alice")
	bobReg := newRegistry("bob")

	createAs := func(t *testing.T, r *Registry, name string) string {
		t.Helper()
		args, _ := json.Marshal(createCronJobArgs{
			Name:     name,
			Type:     "once",
			Schedule: time.Now().Add(time.Hour).Format(time.RFC3339),
			Message:  "提醒",
		})
		out, err := r.Execute(context.Background(), "create_cron_job", string(args))
		if err != nil {
			t.Fatalf("create cron job: %v", err)
		}
		// Response contains "ID: <uuid>"; extract it.
		idx := strings.Index(out, "ID: ")
		if idx < 0 {
			t.Fatalf("create output missing ID: %q", out)
		}
		id := strings.TrimSpace(out[idx+4:])
		if i := strings.IndexByte(id, '\n'); i >= 0 {
			id = id[:i]
		}
		return id
	}

	listAs := func(t *testing.T, r *Registry) []store.CronJobRecord {
		t.Helper()
		out, err := r.Execute(context.Background(), "list_cron_jobs", "{}")
		if err != nil {
			t.Fatalf("list cron jobs: %v", err)
		}
		if strings.Contains(out, "No cron jobs") {
			return nil
		}
		var jobs []store.CronJobRecord
		if err := json.Unmarshal([]byte(out), &jobs); err != nil {
			t.Fatalf("unmarshal list output: %v\nraw: %s", err, out)
		}
		return jobs
	}

	// Alice and Bob each create one job on the same agent.
	aliceID := createAs(t, aliceReg, "alice reminder")
	bobID := createAs(t, bobReg, "bob reminder")

	// Each chatter only sees their own.
	aliceJobs := listAs(t, aliceReg)
	if len(aliceJobs) != 1 || aliceJobs[0].ID != aliceID {
		t.Fatalf("alice sees %v, want only her job %s", aliceJobs, aliceID)
	}
	bobJobs := listAs(t, bobReg)
	if len(bobJobs) != 1 || bobJobs[0].ID != bobID {
		t.Fatalf("bob sees %v, want only his job %s", bobJobs, bobID)
	}

	// Bob cannot delete Alice's job — it's reported as not found (no
	// existence leak) and the row survives.
	delArgs, _ := json.Marshal(deleteCronJobArgs{ID: aliceID})
	if _, err := bobReg.Execute(context.Background(), "delete_cron_job", string(delArgs)); err == nil {
		t.Fatalf("bob was allowed to delete alice's job (cross-chatter leak)")
	}
	if job, err := db.GetCronJob(context.Background(), aliceID); err != nil || job == nil {
		t.Fatalf("alice's job disappeared after bob's delete attempt: %v", err)
	}

	// Alice can delete her own job.
	if _, err := aliceReg.Execute(context.Background(), "delete_cron_job", string(delArgs)); err != nil {
		t.Fatalf("alice failed to delete her own job: %v", err)
	}
}
