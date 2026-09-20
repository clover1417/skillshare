package server

import (
	"os"
	"path/filepath"
	"strings"

	"skillshare/internal/resource"
	ssync "skillshare/internal/sync"
	"skillshare/internal/utils"
)

// computeAgentTargetDiff computes diff items for agents in a single target directory.
// Returns items with Kind="agent" for each pending action (link, update, prune, local).
func computeAgentTargetDiff(targetDir string, agents []resource.DiscoveredResource) []diffItem {
	var items []diffItem

	// Build expected set
	expected := make(map[string]resource.DiscoveredResource, len(agents))
	for _, a := range agents {
		expected[a.FlatName] = a
	}

	// Read existing .md files in target
	existing := make(map[string]os.FileMode)
	if entries, err := os.ReadDir(targetDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			if resource.ConventionalExcludes[e.Name()] {
				continue
			}
			existing[e.Name()] = e.Type()
		}
	}

	// Missing agents → link
	for flatName, agent := range expected {
		fileType, ok := existing[flatName]
		if !ok {
			items = append(items, diffItem{
				Skill:  flatName,
				Action: "link",
				Reason: "source only",
				Kind:   kindAgent,
			})
			continue
		}

		// Exists — check if symlink points to correct source
		targetPath := filepath.Join(targetDir, flatName)
		switch ssync.AgentFileState(targetPath, agent.AbsPath) {
		case "synced":
			continue
		case "update":
			items = append(items, diffItem{Skill: flatName, Action: "update", Reason: "source content changed", Kind: kindAgent})
			continue
		}
		if fileType&os.ModeSymlink != 0 || utils.IsSymlinkOrJunction(targetPath) {
			absLink, err := utils.ResolveLinkTarget(targetPath)
			if err != nil {
				items = append(items, diffItem{
					Skill:  flatName,
					Action: "update",
					Reason: "link target unreadable",
					Kind:   kindAgent,
				})
				continue
			}
			absSource, _ := filepath.Abs(agent.AbsPath)
			if !utils.PathsEqual(absLink, absSource) {
				items = append(items, diffItem{
					Skill:  flatName,
					Action: "update",
					Reason: "symlink points elsewhere",
					Kind:   kindAgent,
				})
			}
			continue
		}

		// Match skills diff semantics: a local file blocks sync unless forced.
		items = append(items, diffItem{
			Skill:  flatName,
			Action: "skip",
			Reason: "local copy (sync --force to replace)",
			Kind:   kindAgent,
		})
	}

	// Orphan/local detection
	for name := range existing {
		if _, ok := expected[name]; ok {
			continue
		}
		targetPath := filepath.Join(targetDir, name)
		if state := ssync.AgentFileState(targetPath, ""); state == "synced" || state == "update" || state == "orphan" {
			items = append(items, diffItem{
				Skill:  name,
				Action: "prune",
				Reason: "orphan managed file",
				Kind:   kindAgent,
			})
		} else {
			items = append(items, diffItem{
				Skill:  name,
				Action: "local",
				Reason: "local file",
				Kind:   kindAgent,
			})
		}
	}

	return items
}
