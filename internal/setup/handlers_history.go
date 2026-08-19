package setup

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// Workspace version history handlers. The feature is opt-in at the WRITE
// side (workspaceHistory.enabled gates the turn-boundary snapshot in the
// agent loop); reading and restoring existing history needs no toggle —
// rollback is a deliberate user action.
//
// Scope keys mirror the agent loop's commit path: chatID for loose chats,
// "<projectID>-<chatID>" for project chats.

// workspaceHistoryScope resolves the URL sessionId to the history scope key
// and the on-disk worktree. Only LocalFS-backed workspaces have on-disk
// scope dirs (probed via the LocalScoper marker, so a Metered wrapper still
// counts), so S3 deployments get a stable 503 (never a 404 that looks
// like "no history yet").
func (s *Server) workspaceHistoryScope(w http.ResponseWriter, r *http.Request, agentID, urlToken string) (scope, workTree string, ok bool) {
	ls, isLocal := s.workspaceStore.(workspace.LocalScoper)
	if !isLocal {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "workspace history requires a local workspace store"})
		return "", "", false
	}
	chatID := s.workspaceSessionScope(r.Context(), agentID, urlToken)
	if chatID == "" {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return "", "", false
	}
	projectID := s.resolveSessionProject(r.Context(), r, agentID, urlToken)
	scope = chatID
	if projectID != "" {
		scope = projectID + "-" + chatID
	}
	workTree, _ = ls.LocalScopeDir(agentID, projectID, chatID)
	return scope, workTree, true
}

func (s *Server) workspaceHistoryRoot() (string, error) {
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "workspace-history"), nil
}

// handleAgentSessionHistory lists the snapshot commits of one chat's
// workspace, newest first: {"history": [{hash, message, time}]}.
func (s *Server) handleAgentSessionHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	scope, _, ok := s.workspaceHistoryScope(w, r, id, r.PathValue("sessionId"))
	if !ok {
		return
	}
	root, err := s.workspaceHistoryRoot()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	entries, err := workspace.NewHistory(root).List(r.Context(), scope)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []workspace.Entry{}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"history": entries})
}

// handleAgentSessionHistoryRestore checks the chat's workspace out to one
// snapshot commit. The commit hash is validated in workspace.History.
func (s *Server) handleAgentSessionHistoryRestore(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	scope, workTree, ok := s.workspaceHistoryScope(w, r, id, r.PathValue("sessionId"))
	if !ok {
		return
	}
	var body struct {
		Commit string `json:"commit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
		return
	}
	root, err := s.workspaceHistoryRoot()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := workspace.NewHistory(root).Restore(r.Context(), scope, workTree, body.Commit); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
