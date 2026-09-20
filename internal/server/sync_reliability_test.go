package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillshare/internal/config"
	"skillshare/internal/testutil"
)

func rootPullFixture(t *testing.T) (*Server, string, string) {
	t.Helper()
	s, _ := newTestServer(t)
	root := config.BaseDir()
	src := filepath.Join(root, "skills")
	dst := t.TempDir()
	client := t.TempDir()
	t.Setenv("HOME", client)
	t.Setenv("USERPROFILE", client)
	t.Setenv("CLAUDE_CONFIG_DIR", client)
	t.Setenv("CODEX_HOME", filepath.Join(client, "codex"))
	for _, dir := range []string{src, filepath.Join(root, "extras", "rules")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	raw := "git_root: root\nsource: " + filepath.ToSlash(src) + "\nmode: copy\ntargets: {}\nextras:\n  - name: rules\n    targets:\n      - path: " + filepath.ToSlash(dst) + "\n        mode: copy\nsources:\n  mcp: " + filepath.ToSlash(filepath.Join(root, "mcp.yaml")) + "\nmcp:\n  targets: [claude]\n"
	files := map[string]string{
		config.ConfigPath(): raw,
		filepath.Join(root, "extras", "rules", "AGENTS.md"): "shared rules",
		filepath.Join(root, "mcp.yaml"):                     "servers:\n  docs:\n    command: echo\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	initServerGitRepo(t, root)
	testutil.RunGit(t, root, "add", "-A")
	testutil.RunGit(t, root, "commit", "-m", "initial resources")
	remote := testutil.SetupBareRemoteRepo(t, t.TempDir())
	testutil.RunGit(t, root, "remote", "add", "origin", remote)
	testutil.RunGit(t, root, "push", "-u", "origin", "HEAD")
	return s, dst, filepath.Join(client, ".claude.json")
}

func TestSyncReliabilityRootPullRepairsExtrasAndMCP(t *testing.T) {
	s, dst, native := rootPullFixture(t)
	for i := 0; i < 2; i++ {
		response := postPull(s, `{}`)
		if response.Code != http.StatusOK {
			t.Fatalf("pull: %d %s", response.Code, response.Body.String())
		}
		var result pullResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || !result.Success || !result.UpToDate {
			t.Fatalf("unexpected result: %s, %v", response.Body.String(), err)
		}
		path := filepath.Join(dst, "AGENTS.md")
		if data, err := os.ReadFile(path); err != nil || string(data) != "shared rules" {
			t.Fatalf("extra not repaired: %q %v", data, err)
		}
		if data, err := os.ReadFile(native); err != nil || !strings.Contains(string(data), "docs") {
			t.Fatalf("MCP missing: %q %v", data, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSyncReliabilityRootPullConflictPreventsExtras(t *testing.T) {
	s, dst, native := rootPullFixture(t)
	if err := os.WriteFile(native, []byte(`{"mcpServers":{"docs":{"command":"private"}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	response := postPull(s, `{}`)
	if response.Code < 400 {
		t.Fatalf("MCP conflict reported success: %s", response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dst, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("extras changed before MCP conflict was handled: %v", err)
	}
}

func TestSyncReliabilityRootPullExtrasErrorPreventsMCP(t *testing.T) {
	s, dst, native := rootPullFixture(t)
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("blocking file"), 0644); err != nil {
		t.Fatal(err)
	}
	response := postPull(s, `{}`)
	if response.Code < 400 {
		t.Fatalf("extras failure reported success: %s", response.Body.String())
	}
	if _, err := os.Stat(native); !os.IsNotExist(err) {
		t.Fatalf("MCP changed after resource failure: %v", err)
	}
}

func TestSyncReliabilityServerDryRunMissingSource(t *testing.T) {
	s, src := newTestServerWithExtras(t, []config.ExtraConfig{{Name: "rules", Targets: []config.ExtraTargetConfig{{Path: t.TempDir(), Mode: "copy"}}}}, "")
	source := filepath.Join(filepath.Dir(src), "extras", "rules")
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/extras/sync", strings.NewReader(`{"dry_run":true}`)))
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("dry run created missing source: %v", err)
	}
	if rr.Code < 400 {
		t.Fatalf("missing source reported success: %s", rr.Body.String())
	}
}
