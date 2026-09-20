package sync

import (
	"fmt"
	"os"
	"path/filepath"
	stdsync "sync"
	"testing"

	"skillshare/internal/resource"
)

func TestFileSyncReadableAndRefreshesReplacedSource(t *testing.T) {
	for _, mode := range []string{"merge", "copy"} {
		t.Run(mode, func(t *testing.T) {
			src, dst := setupExtrasTest(t, map[string]string{"AGENTS.md": "first"})
			for _, content := range []string{"first", "second"} {
				replacement := filepath.Join(src, "replacement")
				if err := os.WriteFile(replacement, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, filepath.Join(src, "AGENTS.md")); err != nil {
					t.Fatal(err)
				}
				result, err := SyncExtra(src, dst, mode, false, false, false, "", nil)
				if err != nil || len(result.Errors) != 0 || result.Skipped != 0 {
					t.Fatalf("sync: %+v, %v", result, err)
				}
				data, err := os.ReadFile(filepath.Join(dst, "AGENTS.md"))
				if err != nil || string(data) != content {
					t.Fatalf("read = %q, %v; want %q", data, err, content)
				}
				if status := CheckSyncStatus([]string{"AGENTS.md"}, src, dst, mode, false, ""); status != "synced" {
					t.Fatalf("status = %s", status)
				}
			}
		})
	}
}

func TestFileSyncCopyPreservesLocalEditsAndOtherSources(t *testing.T) {
	src, dst := setupExtrasTest(t, map[string]string{"AGENTS.md": "first"})
	if _, err := SyncExtra(src, dst, "copy", false, false, false, "", nil); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dst, "AGENTS.md")
	if err := os.WriteFile(target, []byte("local edit"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "AGENTS.md"), []byte("upstream edit"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := SyncExtra(src, dst, "copy", false, false, false, "", nil)
	if err != nil || result.Skipped != 1 {
		t.Fatalf("sync: %+v, %v", result, err)
	}
	if data, _ := os.ReadFile(target); string(data) != "local edit" {
		t.Fatalf("local edit overwritten: %q", data)
	}
	if _, err := SyncExtra(src, dst, "copy", false, true, false, "", nil); err != nil {
		t.Fatal(err)
	}
	other, _ := setupExtrasTest(t, map[string]string{"AGENTS.md": "other source"})
	result, err = SyncExtra(other, dst, "copy", false, false, false, "", nil)
	if err != nil || result.Skipped != 1 {
		t.Fatalf("other source: %+v, %v", result, err)
	}
	if data, _ := os.ReadFile(target); string(data) != "upstream edit" {
		t.Fatalf("other source overwrote file: %q", data)
	}
}

func TestFileSyncCopyDryRunPreservesTarget(t *testing.T) {
	src, dst := setupExtrasTest(t, map[string]string{"AGENTS.md": "first"})
	if _, err := SyncExtra(src, dst, "copy", false, false, false, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "AGENTS.md"), []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := SyncExtra(src, dst, "copy", true, false, false, "", nil)
	if err != nil || result.Synced != 1 || result.Skipped != 0 {
		t.Fatalf("preview: %+v, %v", result, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "AGENTS.md")); string(data) != "first" {
		t.Fatalf("dry run changed file: %q", data)
	}
	result, err = SyncExtra(src, dst, "copy", false, false, false, "", nil)
	if err != nil || result.Skipped != 0 {
		t.Fatalf("apply after preview: %+v, %v", result, err)
	}
}

func TestFileSyncAgentsReadableAndLocalCopyProtected(t *testing.T) {
	for _, mode := range []string{"merge", "copy"} {
		t.Run(mode, func(t *testing.T) {
			src, dst := setupExtrasTest(t, map[string]string{"reviewer.md": "review carefully"})
			agents := []resource.DiscoveredResource{{FlatName: "reviewer.md", AbsPath: filepath.Join(src, "reviewer.md")}}
			if _, err := SyncAgents(agents, src, dst, mode, false, false); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(dst, "reviewer.md")); err != nil || string(data) != "review carefully" {
				t.Fatalf("agent: %q, %v", data, err)
			}
		})
	}
	src, dst := setupExtrasTest(t, map[string]string{"reviewer.md": "shared"})
	if err := os.WriteFile(filepath.Join(dst, "reviewer.md"), []byte("local"), 0644); err != nil {
		t.Fatal(err)
	}
	agents := []resource.DiscoveredResource{{FlatName: "reviewer.md", AbsPath: filepath.Join(src, "reviewer.md")}}
	result, err := SyncAgents(agents, src, dst, "copy", false, false)
	if err != nil || len(result.Skipped) != 1 {
		t.Fatalf("local agent: %+v, %v", result, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "reviewer.md")); string(data) != "local" {
		t.Fatalf("local agent overwritten: %q", data)
	}
}

func TestFileSyncPruneAgentCopiesPreservesUnmanaged(t *testing.T) {
	dst := t.TempDir()
	path := filepath.Join(dst, "private.md")
	if err := os.WriteFile(path, []byte("private"), 0644); err != nil {
		t.Fatal(err)
	}
	removed, err := PruneOrphanAgentCopies(dst, nil, false)
	if err != nil || len(removed) != 0 {
		t.Fatalf("prune: %v, %v", removed, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestFileSyncPruneIsolatesSourcesKindsAndLocalChanges(t *testing.T) {
	for _, mode := range []string{"copy", "merge"} {
		t.Run(mode, func(t *testing.T) {
			src, dst := setupExtrasTest(t, map[string]string{"gone.md": "gone", "edited.md": "original"})
			other, _ := setupExtrasTest(t, map[string]string{"other.md": "other"})
			for _, source := range []string{src, other} {
				if result, err := SyncExtra(source, dst, mode, false, false, false, "", nil); err != nil || len(result.Errors) != 0 {
					t.Fatalf("sync: %+v %v", result, err)
				}
			}
			for _, name := range []string{"gone.md", "edited.md"} {
				if err := os.Remove(filepath.Join(src, name)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(filepath.Join(dst, "edited.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dst, "edited.md"), []byte("local"), 0644); err != nil {
				t.Fatal(err)
			}
			if removed, err := PruneOrphanAgentCopies(dst, nil, false); err != nil || len(removed) != 0 {
				t.Fatalf("agent prune touched extras: %v %v", removed, err)
			}
			result, err := SyncExtra(src, dst, mode, false, false, false, "", nil)
			if err != nil || len(result.Errors) != 0 || result.Pruned != 1 {
				t.Fatalf("prune: %+v %v", result, err)
			}
			for name, content := range map[string]string{"edited.md": "local", "other.md": "other"} {
				if data, err := os.ReadFile(filepath.Join(dst, name)); err != nil || string(data) != content {
					t.Fatalf("preserved %s: %q %v", name, data, err)
				}
			}
		})
	}
}

func TestFileSyncAgentOwnershipAndFlattenedStability(t *testing.T) {
	src, dst := setupExtrasTest(t, map[string]string{"team/reviewer.md": "shared"})
	agents := []resource.DiscoveredResource{{FlatName: "team__reviewer.md", AbsPath: filepath.Join(src, "team", "reviewer.md")}}
	for i := 0; i < 2; i++ {
		result, err := SyncAgents(agents, src, dst, "copy", false, false)
		if err != nil || len(result.Linked) != 1 || len(result.Updated) != 0 {
			t.Fatalf("sync %d: %+v %v", i, result, err)
		}
	}
	if local, err := FindLocalAgents(dst, src); err != nil || len(local) != 0 {
		t.Fatalf("managed copies reported local: %v %v", local, err)
	}
	if removed, err := PruneOrphanAgentCopies(dst, nil, true); err != nil || len(removed) != 1 {
		t.Fatalf("prune preview: %v %v", removed, err)
	}
	if removed, err := PruneOrphanAgentCopies(dst, nil, false); err != nil || len(removed) != 1 {
		t.Fatalf("prune apply: %v %v", removed, err)
	}
}

func TestFileSyncConcurrentManifestAndCorruption(t *testing.T) {
	src, dst := setupExtrasTest(t, nil)
	var group stdsync.WaitGroup
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("%d.md", i)
		if err := os.WriteFile(filepath.Join(src, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
		group.Add(1)
		go func() {
			defer group.Done()
			if _, _, err := syncOneExtraFile(filepath.Join(src, name), filepath.Join(dst, name), "copy", false, false, false); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	entries, err := readFileManifest(dst)
	if err != nil || len(entries) != 8 {
		t.Fatalf("lost manifest updates: %d %v", len(entries), err)
	}
	if err := os.WriteFile(filepath.Join(dst, fileManifestName), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := syncOneExtraFile(filepath.Join(src, "0.md"), filepath.Join(dst, "0.md"), "copy", false, true, false); err == nil {
		t.Fatal("corrupted manifest silently replaced")
	}
	if data, err := os.ReadFile(filepath.Join(dst, "0.md")); err != nil || string(data) != "0.md" {
		t.Fatalf("content changed on manifest failure: %q %v", data, err)
	}
}

func TestFileSyncMissingSourcePreservesManagedTarget(t *testing.T) {
	src, dst := setupExtrasTest(t, map[string]string{"AGENTS.md": "shared"})
	if result, err := SyncExtra(src, dst, "copy", false, false, false, "", nil); err != nil || len(result.Errors) != 0 {
		t.Fatalf("sync: %+v %v", result, err)
	}
	missing := src + "-missing"
	if err := os.Rename(src, missing); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncExtra(src, dst, "copy", false, false, false, "", nil); err == nil {
		t.Fatal("missing source reported success")
	}
	if data, err := os.ReadFile(filepath.Join(dst, "AGENTS.md")); err != nil || string(data) != "shared" {
		t.Fatalf("missing source pruned target: %q %v", data, err)
	}
}

func TestFileSyncTargetRemovalPreservesLocalChanges(t *testing.T) {
	src, dst := setupExtrasTest(t, map[string]string{"changed.md": "initial", "remove.md": "managed"})
	if result, err := SyncExtra(src, dst, "copy", false, false, false, "", nil); err != nil || len(result.Errors) != 0 {
		t.Fatalf("sync: %+v %v", result, err)
	}
	for _, name := range []string{"changed.md", "private.md"} {
		if err := os.WriteFile(filepath.Join(dst, name), []byte("local"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	removed, failures := PruneExtraTargetFiles(dst, "copy", map[string]bool{"changed.md": true, "remove.md": true, "private.md": true})
	if removed != 1 || len(failures) != 0 {
		t.Fatalf("remove target: %d %v", removed, failures)
	}
	for _, name := range []string{"changed.md", "private.md"} {
		if data, err := os.ReadFile(filepath.Join(dst, name)); err != nil || string(data) != "local" {
			t.Fatalf("local file removed: %s %q %v", name, data, err)
		}
	}
}

func TestFileSyncTargetRemovalPreservesOtherSource(t *testing.T) {
	src, dst := setupExtrasTest(t, map[string]string{"shared.md": "first source"})
	other, _ := setupExtrasTest(t, map[string]string{"shared.md": "second source"})
	for _, source := range []string{src, other} {
		if result, err := SyncExtra(source, dst, "copy", false, true, false, "", nil); err != nil || len(result.Errors) != 0 {
			t.Fatalf("sync: %+v %v", result, err)
		}
	}
	removed, failures := PruneExtraTargetFiles(dst, "copy", map[string]bool{"shared.md": true}, src)
	if removed != 0 || len(failures) != 0 {
		t.Fatalf("removed another source's file: %d %v", removed, failures)
	}
	if data, err := os.ReadFile(filepath.Join(dst, "shared.md")); err != nil || string(data) != "second source" {
		t.Fatalf("other source content: %q %v", data, err)
	}
}

func TestFileSyncDirectoryOverlapAndMissingSource(t *testing.T) {
	src, dst := setupExtrasTest(t, map[string]string{"AGENTS.md": "keep"})
	for _, target := range []string{src, filepath.Dir(src), filepath.Join(src, "nested")} {
		if _, err := SyncExtra(src, target, "symlink", false, true, false, "", nil); err == nil {
			t.Fatalf("overlap accepted: %s", target)
		}
		if _, err := SyncAgents(nil, src, target, "symlink", false, true); err == nil {
			t.Fatalf("agent overlap accepted: %s", target)
		}
	}
	if data, err := os.ReadFile(filepath.Join(src, "AGENTS.md")); err != nil || string(data) != "keep" {
		t.Fatalf("source overwritten: %q %v", data, err)
	}
	if _, err := SyncExtra(src+"-missing", dst, "symlink", false, true, false, "", nil); err == nil {
		t.Fatal("missing directory link source accepted")
	}
	if result, err := SyncExtra(src, dst, "symlink", false, true, false, "", nil); err != nil || len(result.Errors) != 0 {
		t.Fatalf("directory link sync: %+v %v", result, err)
	}
	if status := CheckSyncStatus([]string{"AGENTS.md"}, src, dst, "symlink", false, ""); status != "synced" {
		t.Fatalf("directory link status: %s", status)
	}
}
