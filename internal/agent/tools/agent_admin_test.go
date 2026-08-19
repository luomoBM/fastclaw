package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agentcli"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db")+"?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// seedAgent creates an agent and returns (agentID, ownerUserID).
func seedAgent(t *testing.T, st store.Store, name string) (string, string) {
	t.Helper()
	res, err := agentcli.Init(context.Background(), st, name, agentcli.InitOptions{
		Description: "test agent",
	})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return res.Agent.ID, res.Agent.UserID
}

// writeSkill drops a SKILL.md with the given frontmatter body into the
// agent home layout DiagnoseAgent expects.
func writeSkill(t *testing.T, home, name, frontmatter string) {
	t.Helper()
	dir := filepath.Join(home, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: test\n" + frontmatter + "---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiagnoseAgentFlagsSkillWhoseToolIsMissing is the regression guard
// for the delivery failure this whole diagnosis exists for: an agent
// provisioned with an illustration skill, onto a deployment with no
// image-gen credentials. Every individual step succeeded — record
// created, skill installed, files on disk, model set — and the agent
// could not produce a single image.
func TestDiagnoseAgentFlagsSkillWhoseToolIsMissing(t *testing.T) {
	st := newTestStore(t)
	agentID, ownerID := seedAgent(t, st, "illustrator")
	if err := agentcli.SetConfig(context.Background(), st, agentID, "model", "anthropic/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	writeSkill(t, home, "anthropic-art", "metadata:\n  fastclaw:\n    requires:\n      tools: [image_gen]\n")

	deps := AgentAdminDeps{
		Store:        st,
		OwnerUserID:  ownerID,
		AgentHomeDir: func(string) (string, error) { return home, nil },
		ToolAvailability: func(context.Context, string) (map[string]bool, error) {
			return map[string]bool{"image_gen": false, "web_fetch": true}, nil
		},
	}

	report := DiagnoseAgent(context.Background(), deps, agentID, "illustrator")
	if !strings.Contains(report, "VERDICT: NOT ready") {
		t.Fatalf("an agent that cannot run its only skill must not be reported ready:\n%s", report)
	}
	if !strings.Contains(report, "tool:image_gen") {
		t.Errorf("report should name the missing tool:\n%s", report)
	}
	if !strings.Contains(report, "instead of claiming the agent works") {
		t.Errorf("report should forbid claiming success:\n%s", report)
	}

	// Same agent, deployment that does have image-gen credentials.
	deps.ToolAvailability = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{"image_gen": true}, nil
	}
	if report := DiagnoseAgent(context.Background(), deps, agentID, "illustrator"); !strings.Contains(report, "VERDICT: ready") {
		t.Errorf("fully satisfied agent should be ready:\n%s", report)
	}
}

// A missing model is fatal on its own — the agent cannot answer at all.
func TestDiagnoseAgentFlagsMissingModel(t *testing.T) {
	st := newTestStore(t)
	agentID, ownerID := seedAgent(t, st, "no-model")
	home := t.TempDir()

	report := DiagnoseAgent(context.Background(), AgentAdminDeps{
		Store:        st,
		OwnerUserID:  ownerID,
		AgentHomeDir: func(string) (string, error) { return home, nil },
		ToolAvailability: func(context.Context, string) (map[string]bool, error) {
			return map[string]bool{}, nil
		},
	}, agentID, "no-model")

	if !strings.Contains(report, "[FAIL] model") || !strings.Contains(report, "VERDICT: NOT ready") {
		t.Fatalf("missing model must fail the diagnosis:\n%s", report)
	}
}

// Without a capability probe the honest answer is "unverified", never
// "ready" — a confident green light is exactly the failure mode being
// fixed.
func TestDiagnoseAgentSaysUnverifiedWithoutProbe(t *testing.T) {
	st := newTestStore(t)
	agentID, ownerID := seedAgent(t, st, "unprobed")
	if err := agentcli.SetConfig(context.Background(), st, agentID, "model", "anthropic/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	writeSkill(t, home, "art", "metadata:\n  fastclaw:\n    requires:\n      tools: [image_gen]\n")

	report := DiagnoseAgent(context.Background(), AgentAdminDeps{
		Store:        st,
		OwnerUserID:  ownerID,
		AgentHomeDir: func(string) (string, error) { return home, nil },
	}, agentID, "unprobed")

	if strings.Contains(report, "VERDICT: ready") {
		t.Fatalf("must not claim ready when tool availability is unknown:\n%s", report)
	}
	if !strings.Contains(report, "unverified") {
		t.Errorf("report should mark the tool unverified:\n%s", report)
	}
}

// TestAgentAdminToolsRefuseNonAdmin pins the trust boundary: the chatter
// driving a turn changes, so admin rights are re-checked per call rather
// than assumed at registration.
func TestAgentAdminToolsRefuseNonAdmin(t *testing.T) {
	st := newTestStore(t)
	_, ownerID := seedAgent(t, st, "owned")

	r := NewRegistry(t.TempDir(), t.TempDir())
	RegisterAgentAdmin(r, AgentAdminDeps{Store: st, OwnerUserID: ownerID})
	r.SetCallerIsAdmin(false)

	for _, tc := range []struct{ tool, args string }{
		{"create_agent", `{"name":"sneaky"}`},
		{"configure_agent", `{"agent":"owned","key":"model","value":"x/y"}`},
		{"check_agent", `{"agent":"owned"}`},
	} {
		fn := r.GetFunc(tc.tool)
		if fn == nil {
			t.Fatalf("%s not registered", tc.tool)
		}
		if _, err := fn(context.Background(), json.RawMessage(tc.args)); err == nil {
			t.Errorf("%s must refuse a non-admin chatter", tc.tool)
		}
	}

	// And the same calls succeed for an admin.
	r.SetCallerIsAdmin(true)
	if _, err := r.GetFunc("check_agent")(context.Background(), json.RawMessage(`{"agent":"owned"}`)); err != nil {
		t.Errorf("check_agent should work for an admin: %v", err)
	}
}

// An agent belonging to another account must be untouchable regardless of
// admin status on the calling agent — the model chooses this argument, so
// a hallucinated or user-supplied ID must not cross a tenant boundary.
func TestAgentAdminRefusesForeignAgent(t *testing.T) {
	st := newTestStore(t)
	seedAgent(t, st, "theirs")

	r := NewRegistry(t.TempDir(), t.TempDir())
	RegisterAgentAdmin(r, AgentAdminDeps{Store: st, OwnerUserID: "u_someone_else"})
	r.SetCallerIsAdmin(true)

	_, err := r.GetFunc("configure_agent")(context.Background(), json.RawMessage(`{"agent":"theirs","key":"model","value":"x/y"}`))
	if err == nil {
		t.Fatal("expected refusal when the target agent has a different owner")
	}
	if !strings.Contains(err.Error(), "different account") {
		t.Errorf("error should explain the ownership refusal, got: %v", err)
	}
}

// create_agent must pin ownership to the calling account. agentcli.Init's
// convenience fallback picks "the first active super_admin" when no owner
// is named, which in a multi-tenant deployment would hand the new agent
// to the wrong user.
func TestCreateAgentPinsOwnership(t *testing.T) {
	st := newTestStore(t)
	_, ownerID := seedAgent(t, st, "seed")

	r := NewRegistry(t.TempDir(), t.TempDir())
	RegisterAgentAdmin(r, AgentAdminDeps{Store: st, OwnerUserID: ownerID})
	r.SetCallerIsAdmin(true)

	out, err := r.GetFunc("create_agent")(context.Background(), json.RawMessage(`{"name":"fresh","description":"d"}`))
	if err != nil {
		t.Fatalf("create_agent: %v", err)
	}
	if !strings.Contains(out, "NO model is configured") {
		t.Errorf("a model-less new agent must be reported as not yet usable, got: %s", out)
	}
	rec, err := agentcli.Resolve(context.Background(), st, "fresh")
	if err != nil {
		t.Fatalf("resolve created agent: %v", err)
	}
	if rec.UserID != ownerID {
		t.Errorf("new agent owner = %q, want the calling account %q", rec.UserID, ownerID)
	}
}

// TestDiagnoseAgentZeroSkillsIsNotAConfidentReady covers the case that
// slipped through in the field: a provisioning run whose skill install
// landed in the wrong directory, on a check that answered "ready".
//
// Zero skills is legitimate for a plain conversational agent, so this is
// not a hard failure — but it must never read as an unqualified green
// light, and the report has to name the directory it scanned so the
// finding can't be argued away.
func TestDiagnoseAgentZeroSkillsIsNotAConfidentReady(t *testing.T) {
	st := newTestStore(t)
	agentID, ownerID := seedAgent(t, st, "bare")
	if err := agentcli.SetConfig(context.Background(), st, agentID, "model", "anthropic/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	deps := AgentAdminDeps{
		Store:        st,
		OwnerUserID:  ownerID,
		AgentHomeDir: func(string) (string, error) { return home, nil },
		ToolAvailability: func(context.Context, string) (map[string]bool, error) {
			return map[string]bool{}, nil
		},
	}

	report := DiagnoseAgent(context.Background(), deps, agentID, "bare")
	if strings.Contains(report, "VERDICT: ready.") {
		t.Fatalf("zero skills must not render as an unqualified ready:\n%s", report)
	}
	if !strings.Contains(report, "did NOT land here") {
		t.Errorf("report should flag a provisioning miss:\n%s", report)
	}
	if !strings.Contains(report, filepath.Join(home, "skills")) {
		t.Errorf("report must name the scanned directory so it can be checked:\n%s", report)
	}
}

// expect_skills turns the check from "describe what's there" into
// "confirm what was intended" — the only version that catches a skill
// written one directory too high.
func TestDiagnoseAgentFailsOnMissingExpectedSkill(t *testing.T) {
	st := newTestStore(t)
	agentID, ownerID := seedAgent(t, st, "expects")
	if err := agentcli.SetConfig(context.Background(), st, agentID, "model", "anthropic/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	deps := AgentAdminDeps{
		Store:        st,
		OwnerUserID:  ownerID,
		AgentHomeDir: func(string) (string, error) { return home, nil },
		ToolAvailability: func(context.Context, string) (map[string]bool, error) {
			return map[string]bool{"image_gen": true}, nil
		},
	}

	report := DiagnoseAgent(context.Background(), deps, agentID, "expects", "anthropic-art")
	if !strings.Contains(report, "VERDICT: NOT ready") {
		t.Fatalf("a missing expected skill must fail the check:\n%s", report)
	}
	if !strings.Contains(report, "expected but NOT present") {
		t.Errorf("report should say the skill was expected:\n%s", report)
	}

	// Now actually install it where the agent reads from.
	writeSkill(t, home, "anthropic-art", "metadata:\n  fastclaw:\n    requires:\n      tools: [image_gen]\n")
	report = DiagnoseAgent(context.Background(), deps, agentID, "expects", "anthropic-art")
	if !strings.Contains(report, "VERDICT: ready.") {
		t.Errorf("expected skill present and satisfied should be ready:\n%s", report)
	}

	// Case-insensitive, since folder names and typed names differ.
	if r := DiagnoseAgent(context.Background(), deps, agentID, "expects", "Anthropic-Art"); !strings.Contains(r, "VERDICT: ready.") {
		t.Errorf("expected-skill matching should be case-insensitive:\n%s", r)
	}
}
