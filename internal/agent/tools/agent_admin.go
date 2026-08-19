package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agentcli"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// AgentAdminDeps carries what the provisioning tools need from the layers
// above. Everything except Store is optional; a nil field degrades that
// one capability into an explicit "unknown" rather than a wrong answer.
type AgentAdminDeps struct {
	Store store.Store

	// OwnerUserID is the account new agents belong to. Provisioning is
	// always scoped to it — a chatter can create agents for themselves
	// and nobody else.
	OwnerUserID string

	// AgentHomeDir resolves an agent ID to its home directory, whose
	// skills/ subdirectory is that agent's private skill scope.
	AgentHomeDir func(agentID string) (string, error)

	// ToolAvailability reports which agent tools a given agent would have
	// at runtime. Tool registration depends on provider credentials
	// (image_gen appears only when image-gen keys are configured), so
	// this is the only way to answer "can that agent actually do the
	// thing its skill promises" from outside that agent's own registry.
	// Nil means the probe is unavailable and check_agent says so instead
	// of guessing.
	ToolAvailability func(ctx context.Context, agentID string) (map[string]bool, error)

	// OnChanged is invoked after a mutation so a running gateway can pick
	// the change up without a restart.
	OnChanged func()
}

// RegisterAgentAdmin wires the in-chat agent provisioning tools.
//
// These exist because provisioning previously had no tool at all: the
// only way to create an agent, give it a skill and set its model was for
// the model to compose `fastclaw ...` command lines and hand them to
// exec. That works exactly when exec reaches the operator's host, which
// makes an ordinary product capability an accident of deployment mode —
// under an enforced sandbox the CLI is not in the container, ~/.fastclaw
// is not mounted, and the whole flow is simply impossible. Going through
// the same Go API the CLI and the admin UI use makes provisioning behave
// identically in all three modes.
//
// Every tool re-checks admin rights at call time rather than trusting
// registration: the registry is long-lived and the chatter driving it
// changes per turn.
func RegisterAgentAdmin(r *Registry, deps AgentAdminDeps) {
	if r == nil || deps.Store == nil {
		return
	}

	requireAdmin := func(op string) error {
		if !r.callerIsAdmin {
			return fmt.Errorf("%s is restricted to the agent operator — the current chatter is not an admin of this agent, so nothing was changed", op)
		}
		if deps.OwnerUserID == "" {
			return fmt.Errorf("%s is unavailable: no owner account is bound to this session", op)
		}
		return nil
	}

	r.Register(
		"create_agent",
		"Create a NEW agent owned by the current user. Use this when the user asks you to build/set up/provision another agent. "+
			"Returns the new agent's ID, which you pass to configure_agent, install_skill(agent=...) and check_agent. "+
			"Creating an agent does NOT make it functional on its own — it still needs a model, and any skill it depends on. "+
			"Finish with check_agent before telling the user it is ready.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Display name for the new agent, e.g. 'anthropic-art'.",
				},
				"description": map[string]interface{}{
					"type":        "string",
					"description": "One-line description of what the agent does.",
				},
				"model": map[string]interface{}{
					"type":        "string",
					"description": "Optional '<provider>/<model>' to configure immediately, e.g. 'anthropic/claude-sonnet-5'. Omit to set it later with configure_agent.",
				},
			},
			"required": []string{"name"},
		},
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var p struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Model       string `json:"model"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return "", err
			}
			if err := requireAdmin("create_agent"); err != nil {
				return "", err
			}
			if strings.TrimSpace(p.Name) == "" {
				return "", fmt.Errorf("name is required")
			}
			res, err := agentcli.Init(ctx, deps.Store, p.Name, agentcli.InitOptions{
				Description: p.Description,
				Model:       p.Model,
				OwnerUserID: deps.OwnerUserID,
			})
			if err != nil {
				return "", err
			}
			if deps.OnChanged != nil {
				deps.OnChanged()
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Created agent %q (id %s).", res.Agent.Name, res.Agent.ID)
			if res.ModelSaved {
				fmt.Fprintf(&b, " Model set to %s.", p.Model)
			} else {
				b.WriteString(" NO model is configured yet — it cannot answer until one is set via configure_agent.")
			}
			b.WriteString(" Next: install any skills it needs with install_skill(agent=\"" + res.Agent.ID + "\"), then run check_agent to confirm it is actually usable.")
			return b.String(), nil
		},
	)

	r.Register(
		"configure_agent",
		"Set a configuration value on an agent you own (most commonly its model), or write its identity files. "+
			"Use key='model' with value='<provider>/<model>'. To write persona files use file='IDENTITY.md' or 'SOUL.md' with content.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent": map[string]interface{}{
					"type":        "string",
					"description": "Target agent name or agt_ id.",
				},
				"key": map[string]interface{}{
					"type":        "string",
					"description": "Config key to set, e.g. 'model'. Omit when writing a file instead.",
				},
				"value": map[string]interface{}{
					"type":        "string",
					"description": "Value for `key`.",
				},
				"file": map[string]interface{}{
					"type":        "string",
					"description": "Identity file to write: IDENTITY.md, SOUL.md or AGENTS.md.",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "Full contents for `file`.",
				},
			},
			"required": []string{"agent"},
		},
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var p struct {
				Agent   string `json:"agent"`
				Key     string `json:"key"`
				Value   string `json:"value"`
				File    string `json:"file"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return "", err
			}
			if err := requireAdmin("configure_agent"); err != nil {
				return "", err
			}
			rec, err := resolveOwnedAgent(ctx, deps, p.Agent)
			if err != nil {
				return "", err
			}
			done := make([]string, 0, 2)
			if p.Key != "" {
				if err := agentcli.SetConfig(ctx, deps.Store, rec.ID, p.Key, p.Value); err != nil {
					return "", err
				}
				done = append(done, fmt.Sprintf("set %s=%s", p.Key, p.Value))
			}
			if p.File != "" {
				if err := agentcli.PutFile(ctx, deps.Store, rec.ID, rec.UserID, p.File, []byte(p.Content)); err != nil {
					return "", err
				}
				done = append(done, fmt.Sprintf("wrote %s (%d bytes)", p.File, len(p.Content)))
			}
			if len(done) == 0 {
				return "", fmt.Errorf("nothing to do: pass key+value, or file+content")
			}
			if deps.OnChanged != nil {
				deps.OnChanged()
			}
			return fmt.Sprintf("Agent %s (%s): %s.", rec.Name, rec.ID, strings.Join(done, "; ")), nil
		},
	)

	r.Register(
		"check_agent",
		"Verify that an agent you own is actually able to do its job: model configured, skills installed, and the tools those skills declare they need actually available. "+
			"Run this before reporting to the user that a newly provisioned agent is ready — installing a skill does NOT guarantee the agent can run it. "+
			"Always pass expect_skills listing the skills you installed this turn; without it the check can only describe what it finds, and a skill written to the wrong place looks identical to one that was never requested.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent": map[string]interface{}{
					"type":        "string",
					"description": "Target agent name or agt_ id.",
				},
				"expect_skills": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Skills that MUST be present, e.g. the ones you just installed. Any that are missing are reported as failures.",
				},
			},
			"required": []string{"agent"},
		},
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var p struct {
				Agent        string   `json:"agent"`
				ExpectSkills []string `json:"expect_skills"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return "", err
			}
			if err := requireAdmin("check_agent"); err != nil {
				return "", err
			}
			rec, err := resolveOwnedAgent(ctx, deps, p.Agent)
			if err != nil {
				return "", err
			}
			return DiagnoseAgent(ctx, deps, rec.ID, rec.Name, p.ExpectSkills...), nil
		},
	)
}

// resolveOwnedAgent looks an agent up and refuses to touch one belonging
// to somebody else. Ownership is re-checked on every call because the
// model picks the target: a hallucinated or user-supplied agent ID must
// not become a write into another tenant's configuration.
func resolveOwnedAgent(ctx context.Context, deps AgentAdminDeps, ref string) (*store.AgentRecord, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("agent is required")
	}
	rec, err := agentcli.Resolve(ctx, deps.Store, ref)
	if err != nil {
		return nil, err
	}
	if rec.UserID != deps.OwnerUserID {
		return nil, fmt.Errorf("agent %q belongs to a different account — refusing to modify it", ref)
	}
	return rec, nil
}

// DiagnoseAgent is the substance behind check_agent: a report of what
// the agent can actually do, not what has been written down about it.
// Exported so `fastclaw agents doctor` renders the identical report —
// one implementation, so the CLI and the in-chat check can never
// disagree about whether an agent is ready.
//
// The gap it closes is a real one. A provisioning run can create an
// agent, install an illustration skill, set a model, write a persona,
// confirm every file is on disk — and hand over an agent that cannot
// draw, because image_gen is registered only when image-gen credentials
// exist and nothing ever checked. Every individual step succeeded; the
// deliverable did not work. So this reports capability, and says
// "unknown" where it cannot tell rather than implying health.
// expectSkills names skills the caller believes it installed. Passing
// them turns the check from "describe what's there" into "confirm what
// was intended", which is the only version that catches a skill written
// to the wrong directory.
func DiagnoseAgent(ctx context.Context, deps AgentAdminDeps, agentID, agentName string, expectSkills ...string) string {
	var b strings.Builder
	problems := 0

	fmt.Fprintf(&b, "Diagnosis for agent %s (%s):\n", agentName, agentID)

	model, err := agentcli.GetConfig(ctx, deps.Store, agentID, "model")
	modelStr, _ := model.(string)
	switch {
	case err != nil || strings.TrimSpace(modelStr) == "":
		b.WriteString("  [FAIL] model: not configured — the agent cannot answer at all\n")
		problems++
	default:
		fmt.Fprintf(&b, "  [ok]   model: %s\n", modelStr)
	}

	// Always name the directory that was scanned. Without it the report
	// is unfalsifiable prose, and a model that dislikes the answer can
	// talk itself out of it — one did, deciding this check "only reads
	// registry metadata" and overriding a correct "no skills found" that
	// was pointing straight at a skill copied one directory too high.
	skillRoot := agentSkillRoot(deps, agentID)
	skills := discoverAgentSkills(deps, agentID)
	if len(skills) == 0 {
		fmt.Fprintf(&b, "  [--]   skills: none found in %s (this is the ONLY directory this agent loads private skills from)\n",
			displayPath(skillRoot))
	}

	// A skill the caller expected but cannot find is a hard failure, and
	// the most likely cause is that it was written somewhere the agent
	// does not read.
	for _, want := range expectSkills {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		if !hasSkillNamed(skills, want) {
			problems++
			fmt.Fprintf(&b, "  [FAIL] skill %s: expected but NOT present in %s — it was not installed where this agent reads from; re-install it with install_skill(agent=…) rather than copying files by hand\n",
				want, displayPath(skillRoot))
		}
	}

	var avail map[string]bool
	var availErr error
	if deps.ToolAvailability != nil {
		avail, availErr = deps.ToolAvailability(ctx, agentID)
	}

	for _, s := range skills {
		unmet := make([]string, 0)
		for _, env := range s.Requires.Env {
			if os.Getenv(env) == "" {
				unmet = append(unmet, "env:"+env)
			}
		}
		for _, tool := range s.Requires.Tools {
			switch {
			case deps.ToolAvailability == nil || availErr != nil:
				unmet = append(unmet, "tool:"+tool+"(unverified)")
			case !avail[tool]:
				unmet = append(unmet, "tool:"+tool)
			}
		}
		if len(unmet) == 0 {
			fmt.Fprintf(&b, "  [ok]   skill %s\n", s.Name)
			continue
		}
		problems++
		fmt.Fprintf(&b, "  [FAIL] skill %s: unmet requirements: %s\n", s.Name, strings.Join(unmet, ", "))
	}

	switch {
	case problems > 0:
		fmt.Fprintf(&b, "VERDICT: NOT ready — %d problem(s) above. Report these to the user instead of claiming the agent works.\n", problems)
	case len(skills) == 0:
		// "Model configured, zero skills" is a legitimate state for a
		// plain conversational agent, so this is not a failure — but it
		// must never render as an unqualified "ready". A provisioning run
		// whose whole purpose was installing a skill got exactly that
		// green light on an agent with no skills, and passed it on to the
		// user as success.
		b.WriteString("VERDICT: ready as a plain agent — but NO skills are installed. " +
			"If this agent was just provisioned with a skill, that install did NOT land here and the task is not done.\n")
	case deps.ToolAvailability == nil:
		b.WriteString("VERDICT: configuration looks complete, but tool availability could not be verified in this deployment. Do not claim the agent is proven working.\n")
	default:
		b.WriteString("VERDICT: ready.\n")
	}
	return b.String()
}

// hasSkillNamed matches case-insensitively; skill directory names and the
// names people type them by differ in case often enough to matter.
func hasSkillNamed(skills []agentSkillInfo, name string) bool {
	for _, s := range skills {
		if strings.EqualFold(s.Name, name) {
			return true
		}
	}
	return false
}

// agentSkillRoot is the one directory an agent loads private skills from.
// Returns "" when it can't be resolved, which displayPath renders as an
// explicit unknown rather than an empty string.
func agentSkillRoot(deps AgentAdminDeps, agentID string) string {
	if deps.AgentHomeDir == nil {
		return ""
	}
	home, err := deps.AgentHomeDir(agentID)
	if err != nil {
		return ""
	}
	return filepath.Join(home, "skills")
}

func displayPath(p string) string {
	if p == "" {
		return "<agent skills dir: unresolved>"
	}
	return p
}

type agentSkillInfo struct {
	Name     string
	Requires SkillRequirements
}

// discoverAgentSkills reads the target agent's private skills directory.
// Best-effort: an unreadable directory yields no skills rather than an
// error, since a missing skills dir just means none were installed.
func discoverAgentSkills(deps AgentAdminDeps, agentID string) []agentSkillInfo {
	root := agentSkillRoot(deps, agentID)
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]agentSkillInfo, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		out = append(out, agentSkillInfo{Name: e.Name(), Requires: ParseSkillRequirements(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
