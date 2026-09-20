package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"skillshare/internal/config"
)

func TestHandleDiff_Empty(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/diff", nil)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Diffs []any `json:"diffs"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Diffs) != 0 {
		t.Errorf("expected 0 diffs, got %d", len(resp.Diffs))
	}
}

func TestHandleDiff_WithTarget(t *testing.T) {
	tgtPath := filepath.Join(t.TempDir(), "claude-skills")
	s, src := newTestServerWithTargets(t, map[string]string{"claude": tgtPath})
	addSkill(t, src, "alpha")

	req := httptest.NewRequest(http.MethodGet, "/api/diff", nil)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Diffs []map[string]any `json:"diffs"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Diffs) != 1 {
		t.Fatalf("expected 1 diff target, got %d", len(resp.Diffs))
	}
}

func TestHandleDiff_AgentPruneWhenSourceEmpty(t *testing.T) {
	s, _ := newTestServer(t)

	agentSource := filepath.Join(t.TempDir(), "agents")
	agentTarget := filepath.Join(t.TempDir(), "claude-agents")
	if err := os.MkdirAll(agentSource, 0o755); err != nil {
		t.Fatalf("mkdir agent source: %v", err)
	}
	if err := os.MkdirAll(agentTarget, 0o755); err != nil {
		t.Fatalf("mkdir agent target: %v", err)
	}
	seedManagedAgent(t, filepath.Join(agentSource, "tutor.md"), filepath.Join(agentTarget, "tutor.md"))

	s.cfg.AgentsSource = agentSource
	s.cfg.Targets["claude"] = config.TargetConfig{
		Skills: &config.ResourceTargetConfig{Path: filepath.Join(t.TempDir(), "claude-skills")},
		Agents: &config.ResourceTargetConfig{Path: agentTarget},
	}
	if err := s.cfg.Save(); err != nil {
		t.Fatalf("save config: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/diff", nil)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Diffs []struct {
			Target string `json:"target"`
			Items  []struct {
				Skill  string `json:"skill"`
				Action string `json:"action"`
				Kind   string `json:"kind"`
			} `json:"items"`
		} `json:"diffs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal diff response: %v", err)
	}

	if len(resp.Diffs) != 1 {
		t.Fatalf("expected 1 diff target, got %d", len(resp.Diffs))
	}
	if resp.Diffs[0].Target != "claude" {
		t.Fatalf("expected claude target, got %q", resp.Diffs[0].Target)
	}
	if len(resp.Diffs[0].Items) != 1 {
		t.Fatalf("expected 1 diff item, got %d", len(resp.Diffs[0].Items))
	}
	item := resp.Diffs[0].Items[0]
	if item.Skill != "tutor.md" || item.Action != "prune" || item.Kind != "agent" {
		t.Fatalf("unexpected diff item: %+v", item)
	}
}
