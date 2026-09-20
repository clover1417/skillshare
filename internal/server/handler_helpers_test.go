package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillshare/internal/config"
	"skillshare/internal/install"
	"skillshare/internal/resource"
	ssync "skillshare/internal/sync"
)

// newTestServer creates an isolated Server for handler testing.
// It sets up a temp source directory and config file, returning the server
// and the source directory path for test setup.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "skills")
	homeDir := filepath.Join(tmp, "home")
	os.MkdirAll(sourceDir, 0755)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg-config"))
	if os.Getenv("HOME") == "" {
		os.MkdirAll(homeDir, 0755)
		t.Setenv("HOME", homeDir)
	}

	cfgPath := filepath.Join(tmp, "config", "config.yaml")
	t.Setenv("SKILLSHARE_CONFIG", cfgPath)
	os.MkdirAll(filepath.Dir(cfgPath), 0755)

	raw := "source: " + sourceDir + "\nmode: merge\ntargets: {}\n"
	os.WriteFile(cfgPath, []byte(raw), 0644)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	s := New(cfg, "127.0.0.1:0", "", "")
	return s, sourceDir
}

// newTestServerWithTargets creates a test server with pre-configured targets.
func newTestServerWithTargets(t *testing.T, targets map[string]string) (*Server, string) {
	t.Helper()
	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "skills")
	homeDir := filepath.Join(tmp, "home")
	os.MkdirAll(sourceDir, 0755)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg-config"))
	if os.Getenv("HOME") == "" {
		os.MkdirAll(homeDir, 0755)
		t.Setenv("HOME", homeDir)
	}

	cfgPath := filepath.Join(tmp, "config", "config.yaml")
	t.Setenv("SKILLSHARE_CONFIG", cfgPath)
	os.MkdirAll(filepath.Dir(cfgPath), 0755)

	raw := "source: " + sourceDir + "\nmode: merge\ntargets:\n"
	for name, path := range targets {
		os.MkdirAll(path, 0755)
		raw += "  " + name + ":\n    path: " + path + "\n"
	}
	os.WriteFile(cfgPath, []byte(raw), 0644)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	s := New(cfg, "127.0.0.1:0", "", "")
	return s, sourceDir
}

// addSkill creates a skill directory with SKILL.md in the source directory.
func addSkill(t *testing.T, sourceDir, name string) {
	t.Helper()
	skillDir := filepath.Join(sourceDir, name)
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: "+name+"\n---\n# "+name), 0644)
}

func addTrackedRepo(t *testing.T, sourceDir, relPath string) {
	t.Helper()
	repoDir := filepath.Join(sourceDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0755); err != nil {
		t.Fatalf("failed to create tracked repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("tracked repo"), 0644); err != nil {
		t.Fatalf("failed to seed tracked repo: %v", err)
	}
}

// addSkillMeta writes a metadata entry into the centralized .metadata.json store.
func addSkillMeta(t *testing.T, sourceDir, name, source string) {
	t.Helper()
	store := install.LoadMetadataOrNew(sourceDir)
	store.Set(name, &install.MetadataEntry{Source: source})
	if err := store.Save(sourceDir); err != nil {
		t.Fatalf("addSkillMeta: %v", err)
	}
}

func addAgent(t *testing.T, agentsDir, relPath string) {
	t.Helper()
	agentPath := filepath.Join(agentsDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o755); err != nil {
		t.Fatalf("create agent dir: %v", err)
	}
	if err := os.WriteFile(agentPath, []byte("---\nname: "+strings.TrimSuffix(filepath.Base(relPath), ".md")+"\n---\n# agent"), 0o644); err != nil {
		t.Fatalf("write agent: %v", err)
	}
}

func addAgentMeta(t *testing.T, agentsDir, relPath, source string) {
	t.Helper()
	store := install.LoadMetadataOrNew(agentsDir)
	key := strings.TrimSuffix(filepath.ToSlash(relPath), ".md")
	store.Set(key, &install.MetadataEntry{
		Source: source,
		Kind:   install.MetadataKindAgent,
		Subdir: filepath.ToSlash(relPath),
	})
	if err := store.Save(agentsDir); err != nil {
		t.Fatalf("addAgentMeta: %v", err)
	}
}

func seedManagedAgent(t *testing.T, source, target string) {
	t.Helper()
	_, statErr := os.Stat(source)
	if os.IsNotExist(statErr) {
		if err := os.WriteFile(source, []byte("# Agent"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	agents := []resource.DiscoveredResource{{FlatName: filepath.Base(target), AbsPath: source}}
	if _, err := ssync.SyncAgents(agents, filepath.Dir(source), filepath.Dir(target), "merge", false, false); err != nil {
		t.Fatal(err)
	}
	if os.IsNotExist(statErr) {
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
	}
}
