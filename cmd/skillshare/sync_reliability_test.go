package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"skillshare/internal/config"
)

func reliabilityConfig(t *testing.T, missing bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	path := filepath.Join(root, "config.yaml")
	t.Setenv("SKILLSHARE_CONFIG", path)
	src := filepath.Join(root, "skills")
	extra := filepath.Join(root, "extras", "rules")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if !missing {
		if err := os.MkdirAll(extra, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(extra, "AGENTS.md"), []byte("instructions"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("blocking file"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	raw := "source: " + filepath.ToSlash(src) + "\nmode: copy\ntargets: {}\nextras:\n  - name: rules\n    targets:\n      - path: " + filepath.ToSlash(target) + "\n        mode: copy\n"
	raw += "mcp:\n  targets: [claude]\n  servers:\n    test-server:\n      command: echo\n"
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	return root, extra
}

func TestSyncReliabilityExtrasJSONFailure(t *testing.T) {
	reliabilityConfig(t, false)
	var err error
	output := captureStdout(t, func() {
		err = cmdSyncExtrasGlobal(false, false, true, time.Now())
	})
	if err == nil || !json.Valid([]byte(output)) {
		t.Fatalf("expected error and valid JSON; err=%v output=%s", err, output)
	}
}

func TestSyncReliabilityDryRunMissingSource(t *testing.T) {
	_, source := reliabilityConfig(t, true)
	var err error
	output := captureStdout(t, func() {
		err = cmdSyncExtrasGlobal(true, false, true, time.Now())
	})
	if _, statErr := os.Stat(source); !os.IsNotExist(statErr) {
		t.Fatalf("dry run created missing source: %v", statErr)
	}
	if err == nil || !json.Valid([]byte(output)) {
		t.Fatalf("missing source must fail with JSON: %v %s", err, output)
	}
}

func TestSyncReliabilityAllExtrasFailureBlocksMCP(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonOutput], func(t *testing.T) {
			reliabilityConfig(t, false)
			args := []string{"--all", "--global"}
			if jsonOutput {
				args = append(args, "--json")
			}
			var err error
			output := captureStdout(t, func() { err = cmdSync(args) })
			if err == nil {
				t.Fatalf("extras failure reported success: %s", output)
			}
			if jsonOutput && !json.Valid([]byte(output)) {
				t.Fatalf("invalid JSON: %s", output)
			}
			path := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".claude.json")
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("MCP applied after extras failure: %v", err)
			}
		})
	}
}

func TestSyncReliabilityProjectExtrasJSONFailure(t *testing.T) {
	root, _ := reliabilityConfig(t, false)
	path := config.ProjectConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	raw := "sources:\n  extras: extras\ntargets: []\nextras:\n  - name: rules\n    targets:\n      - path: target\n        mode: copy\n"
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	var err error
	output := captureStdout(t, func() { err = cmdSyncExtrasProject(root, false, false, true, time.Now()) })
	if err == nil || !json.Valid([]byte(output)) {
		t.Fatalf("expected error and valid JSON: %v %s", err, output)
	}
}
