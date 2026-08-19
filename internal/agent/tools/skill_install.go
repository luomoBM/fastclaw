package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// RegisterSkillInstall wires the per-agent skill search and install tools
// into registry r.
//
// agentSkillsDir is the per-agent skills directory (conventionally
// <agentHome>/skills). Agent-initiated installs always land under that path —
// never in the global ~/.fastclaw/skills/ — so one agent can't alter another
// agent's capabilities just by chatting.
//
// onReload is called after a successful install so the owning agent can
// re-scan its skills dir and expose the new skill on the next turn without
// a restart. Pass nil to disable hot reload.
// TargetSkillDirFunc resolves an install target other than "this agent",
// returning that agent's private skills directory. Supplying one is what
// lets a provisioning turn create an agent and equip it in the same
// breath. Leaving it nil keeps install_skill scoped to the calling
// agent, which is the safe default — otherwise any agent could rewrite
// another agent's capabilities by naming it.
//
// Implementations must enforce ownership; this package calls it with
// whatever agent reference the model produced.
type TargetSkillDirFunc func(ctx context.Context, agentRef string) (dir string, err error)

func RegisterSkillInstall(r *Registry, agentSkillsDir string, onReload func()) {
	RegisterSkillInstallForTarget(r, agentSkillsDir, nil, onReload)
}

// RegisterSkillInstallForTarget is RegisterSkillInstall plus the ability
// to install into another agent the caller owns.
func RegisterSkillInstallForTarget(r *Registry, agentSkillsDir string, resolveTarget TargetSkillDirFunc, onReload func()) {
	r.Register(
		"search_skills",
		"Search for skills on skills.sh (primary registry) and clawhub.ai. Returns the top matches so you can pick one to install.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Search query (e.g. 'pdf', 'translation', 'web scraping')",
				},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var params struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return "", err
			}
			if params.Query == "" {
				return "", fmt.Errorf("query is required")
			}

			var b strings.Builder
			sh, shErr := skills.SearchSkillsSh(params.Query)
			if shErr == nil && len(sh) > 0 {
				fmt.Fprintf(&b, "skills.sh (%d results):\n", len(sh))
				limit := 10
				if len(sh) < limit {
					limit = len(sh)
				}
				for _, r := range sh[:limit] {
					fmt.Fprintf(&b, "- %s (from %s, %d installs)\n", r.SkillID, r.Source, r.Installs)
				}
			} else if shErr != nil {
				fmt.Fprintf(&b, "skills.sh search error: %v\n", shErr)
			} else {
				b.WriteString("skills.sh: no matches\n")
			}
			return b.String(), nil
		},
	)

	r.Register(
		"install_skill",
		"Install a skill into an agent's private skills directory. Tries skills.sh first, then clawhub.ai, or a GitHub repo when `repo` is set. "+
			"If none has it, returns a not-found error — at that point ask the user whether to build a custom skill with the skill-creator skill instead of retrying. "+
			"Installed skills are scoped to one agent; they do not affect other agents. "+
			"This tool is the ONLY correct way to install a skill: it writes to the exact directory the target agent loads from. Never copy skill files into place by hand — "+
			"the agent home layout is not what it looks like, and a hand-placed skill silently never loads.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type": "string",
					"description": "Registry slug (skills.sh / clawhub). With `repo`, this is the skill FOLDER NAME inside that repo — " +
						"omit it when the repo holds a single skill, even if the manifest sits in a subfolder like `skill/`, and the right directory is found automatically. " +
						"Guessing a folder name that doesn't exist is the most common way this call fails.",
				},
				"repo": map[string]interface{}{
					"type":        "string",
					"description": "GitHub 'owner/repo' to install from instead of the public registries. For a single-skill repo, pass ONLY this and leave `name` unset.",
				},
				"agent": map[string]interface{}{
					"type":        "string",
					"description": "Optional: install into ANOTHER agent you own (name or agt_ id) instead of this one. Use this after create_agent to equip the new agent.",
				},
			},
			// Deliberately no hard requirement: `name` alone (registry) and
			// `repo` alone (whole-repo install) are both valid, and the
			// handler rejects the empty case. Marking `name` required made
			// whole-repo installs unreachable — the model had to invent a
			// folder name, which 404'd, and it fell back to copying files by
			// hand into a directory the agent does not read.
			"required": []string{},
		},
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var params struct {
				Name  string `json:"name"`
				Repo  string `json:"repo"`
				Agent string `json:"agent"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return "", err
			}
			if params.Name == "" && params.Repo == "" {
				return "", fmt.Errorf("name or repo is required")
			}

			targetDir := agentSkillsDir
			targetDesc := "this agent"
			if params.Agent != "" {
				if resolveTarget == nil {
					return "", fmt.Errorf("installing into another agent is not available in this deployment; install_skill can only equip the current agent")
				}
				dir, err := resolveTarget(ctx, params.Agent)
				if err != nil {
					return "", err
				}
				targetDir, targetDesc = dir, "agent "+params.Agent
			}
			if targetDir == "" {
				return "", fmt.Errorf("agent skills directory not configured")
			}

			var (
				result *skills.Result
				err    error
			)
			switch {
			case params.Repo != "":
				result, err = skills.InstallFromGitHubRepo(params.Repo, params.Name, targetDir)
			default:
				result, err = skills.InstallAuto(params.Name, targetDir)
			}
			if err != nil {
				return "", fmt.Errorf("%w — if the user still wants this capability, offer to build a custom skill using the skill-creator skill", err)
			}

			// Only the calling agent's own registry can hot-reload here;
			// a cross-agent install is picked up by the target agent on
			// its next turn.
			if onReload != nil && params.Agent == "" {
				onReload()
			}
			msg := fmt.Sprintf("Installed %q from %s to %s (%d files), scoped to %s.", result.Name, result.Source, result.InstalledAt, result.FilesWritten, targetDesc)
			if result.Version != "" {
				msg += fmt.Sprintf(" Version/ref: %s.", result.Version)
			}
			if params.Agent != "" {
				msg += " Installing a skill does NOT prove the target agent can run it — run check_agent(agent=\"" + params.Agent + "\") before telling the user it is ready."
			} else {
				msg += " It will be picked up on the next turn."
			}
			return msg, nil
		},
	)
}
