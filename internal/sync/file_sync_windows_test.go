package sync

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFileSyncRepairsLegacyWindowsFileJunction(t *testing.T) {
	src, dst := setupExtrasTest(t, map[string]string{"AGENTS.md": "readable"})
	source, target := filepath.Join(src, "AGENTS.md"), filepath.Join(dst, "AGENTS.md")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", target, source).CombinedOutput(); err != nil {
		t.Fatalf("legacy junction: %v %s", err, output)
	}
	result, err := SyncExtra(src, dst, "merge", false, false, false, "", nil)
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("repair: %+v %v", result, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "readable" {
		t.Fatalf("legacy junction not repaired: %q %v", data, err)
	}
}
