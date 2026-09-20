package sync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"skillshare/internal/resource"
	"skillshare/internal/utils"
)

// AgentSyncResult holds the result of syncing agents to a target.
type AgentSyncResult struct {
	Linked  []string // Agents that were symlinked (merge) or copied (copy)
	Skipped []string // Agents that already exist in target (kept local)
	Updated []string // Agents that had broken symlinks fixed or content updated
}

// AgentCollision represents two agents that flatten to the same filename.
type AgentCollision struct {
	FlatName string // The colliding flat name (e.g. "helper.md")
	PathA    string // First agent relative path
	PathB    string // Second agent relative path
}

// LocalAgentInfo describes a local agent file in a target directory.
type LocalAgentInfo struct {
	Name       string
	Path       string
	TargetName string
}

// CheckAgentCollisions detects agents that flatten to the same filename.
func CheckAgentCollisions(agents []resource.DiscoveredResource) []AgentCollision {
	seen := make(map[string]string) // flatName → first relPath
	var collisions []AgentCollision

	for _, a := range agents {
		if prev, ok := seen[a.FlatName]; ok {
			collisions = append(collisions, AgentCollision{
				FlatName: a.FlatName,
				PathA:    prev,
				PathB:    a.RelPath,
			})
		} else {
			seen[a.FlatName] = a.RelPath
		}
	}

	return collisions
}

// SyncAgents dispatches to the appropriate sync mode for agents.
// mode: "merge" (per-file symlinks), "symlink" (whole dir), "copy" (file copy).
// projectRoot enables relative symlinks when non-empty.
func SyncAgents(agents []resource.DiscoveredResource, sourceDir, targetDir, mode string, dryRun, force bool, projectRoot ...string) (*AgentSyncResult, error) {
	root := ""
	if len(projectRoot) > 0 {
		root = projectRoot[0]
	}

	switch mode {
	case "symlink":
		return syncAgentsSymlink(sourceDir, targetDir, dryRun, force, root)
	case "copy":
		return syncAgentsCopy(agents, targetDir, dryRun, force)
	default: // "merge" or ""
		return syncAgentsMerge(agents, sourceDir, targetDir, dryRun, force, root)
	}
}

// linkResolvesToSource reports whether a resolved symlink target (absLink) and a
// source path (absSource) reference the same file. The fast path is a direct
// string compare; the slow path canonicalizes both sides through EvalSymlinks so
// symlinked ancestors (e.g. macOS /var → /private/var) do not make a stable
// relative link look like it points elsewhere. Mirrors isSymlinkToSource in sync.go.
func linkResolvesToSource(absLink, absSource string) bool {
	if utils.PathsEqual(absLink, absSource) {
		return true
	}
	return utils.PathsEqual(utils.ResolveSymlink(absLink), utils.ResolveSymlink(absSource))
}

// syncAgentsMerge creates per-file symlinks in targetDir for each discovered agent.
// Existing non-symlink files are preserved (skipped) unless force is true.
func syncAgentsMerge(agents []resource.DiscoveredResource, sourceDir, targetDir string, dryRun, force bool, projectRoot string) (*AgentSyncResult, error) {
	return syncAgentFiles(agents, targetDir, "merge", dryRun, force, shouldUseRelative(projectRoot, sourceDir, targetDir))
}

// syncAgentsSymlink creates a single directory symlink from targetDir to sourceDir.
// If targetDir already exists as a real directory, it's replaced only with force.
func syncAgentsSymlink(sourceDir, targetDir string, dryRun, force bool, projectRoot string) (*AgentSyncResult, error) {
	if err := validateDirectoryLink(sourceDir, targetDir); err != nil {
		return nil, err
	}
	result := &AgentSyncResult{}
	relative := shouldUseRelative(projectRoot, sourceDir, targetDir)

	if !dryRun {
		if err := os.MkdirAll(filepath.Dir(targetDir), 0755); err != nil {
			return nil, fmt.Errorf("failed to create target parent: %w", err)
		}
	}

	_, err := os.Lstat(targetDir)
	if err == nil {
		if utils.IsSymlinkOrJunction(targetDir) {
			// Already a symlink — check if correct
			absLink, linkErr := utils.ResolveLinkTarget(targetDir)
			if linkErr != nil {
				return nil, fmt.Errorf("failed to resolve link: %w", linkErr)
			}
			absSource, _ := filepath.Abs(sourceDir)

			if linkResolvesToSource(absLink, absSource) {
				dest, _ := os.Readlink(targetDir)
				if !linkNeedsReformat(dest, relative) {
					result.Linked = append(result.Linked, "(directory)")
					return result, nil
				}
				if !dryRun {
					if err := reformatLink(targetDir, sourceDir, relative); err != nil {
						return nil, fmt.Errorf("failed to reformat directory symlink: %w", err)
					}
				}
				result.Updated = append(result.Updated, "(directory)")
				return result, nil
			}

			// Wrong target
			if !dryRun {
				os.Remove(targetDir)
				if err := createLink(targetDir, sourceDir, relative); err != nil {
					return nil, fmt.Errorf("failed to create directory symlink: %w", err)
				}
			}
			result.Updated = append(result.Updated, "(directory)")
		} else {
			// Real directory
			if force {
				if !dryRun {
					os.RemoveAll(targetDir)
					if err := createLink(targetDir, sourceDir, relative); err != nil {
						return nil, fmt.Errorf("failed to create directory symlink: %w", err)
					}
				}
				result.Updated = append(result.Updated, "(directory)")
			} else {
				result.Skipped = append(result.Skipped, "(directory)")
			}
		}
	} else if os.IsNotExist(err) {
		if !dryRun {
			if err := createLink(targetDir, sourceDir, relative); err != nil {
				return nil, fmt.Errorf("failed to create directory symlink: %w", err)
			}
		}
		result.Linked = append(result.Linked, "(directory)")
	} else {
		return nil, fmt.Errorf("failed to check target path: %w", err)
	}

	return result, nil
}

// syncAgentsCopy copies agent .md files to targetDir.
func syncAgentsCopy(agents []resource.DiscoveredResource, targetDir string, dryRun, force bool) (*AgentSyncResult, error) {
	return syncAgentFiles(agents, targetDir, "copy", dryRun, force, false)
}

func syncAgentFiles(agents []resource.DiscoveredResource, targetDir, mode string, dryRun, force, relative bool) (*AgentSyncResult, error) {
	result := &AgentSyncResult{}
	for _, agent := range agents {
		path := filepath.Join(targetDir, agent.FlatName)
		_, beforeErr := os.Lstat(path)
		before := fileInSync(agent.AbsPath, path, mode, relative)
		_, skipped, err := syncManagedFile(agent.AbsPath, path, mode, dryRun, force, relative, "agent")
		if err != nil {
			return nil, fmt.Errorf("sync agent %s: %w", agent.FlatName, err)
		}
		if skipped > 0 {
			result.Skipped = append(result.Skipped, agent.FlatName)
		} else if beforeErr == nil && !before {
			result.Updated = append(result.Updated, agent.FlatName)
		} else {
			result.Linked = append(result.Linked, agent.FlatName)
		}
	}
	return result, nil
}

// SyncAgentsToTarget creates file symlinks in targetDir for each discovered agent.
// Uses merge semantics. Kept for backward compatibility; prefer SyncAgents().
func SyncAgentsToTarget(agents []resource.DiscoveredResource, targetDir string, dryRun, force bool) (*AgentSyncResult, error) {
	return syncAgentsMerge(agents, "", targetDir, dryRun, force, "")
}

// PruneOrphanAgentLinks removes file symlinks in targetDir that don't
// correspond to any discovered agent. For merge mode only.
func PruneOrphanAgentLinks(targetDir string, agents []resource.DiscoveredResource, dryRun bool) ([]string, error) {
	return PruneOrphanAgentCopies(targetDir, agents, dryRun)
}

// PruneOrphanAgentCopies removes copied .md files in targetDir that don't
// correspond to any discovered agent. For copy mode only.
func PruneOrphanAgentCopies(targetDir string, agents []resource.DiscoveredResource, dryRun bool) ([]string, error) {
	expected := make(map[string]bool, len(agents))
	for _, agent := range agents {
		expected[agent.FlatName] = true
	}
	return pruneManagedFiles(targetDir, func(name string, entry managedFile) bool {
		return entry.Kind == "agent" && !expected[name] && !resource.ConventionalExcludes[name]
	}, dryRun)
}

// FindLocalAgents finds local (non-symlinked) agent files in a target directory.
// If the target directory itself is a symlink to sourcePath, it returns no local agents.
func FindLocalAgents(targetDir, sourcePath string) ([]LocalAgentInfo, error) {
	var agents []LocalAgentInfo

	_, err := os.Lstat(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return agents, nil
		}
		return nil, fmt.Errorf("failed to read agent target directory: %w", err)
	}

	if utils.IsSymlinkOrJunction(targetDir) {
		absLink, err := utils.ResolveLinkTarget(targetDir)
		if err != nil {
			return nil, err
		}
		absSource, _ := filepath.Abs(sourcePath)
		if linkResolvesToSource(absLink, absSource) {
			return agents, nil
		}
		resolved, statErr := os.Stat(targetDir)
		if statErr != nil || !resolved.IsDir() {
			return agents, nil
		}
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return agents, nil
		}
		return nil, fmt.Errorf("failed to read agent target directory: %w", err)
	}

	managed, err := readFileManifest(targetDir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		name := entry.Name()

		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		if utils.IsHidden(name) || resource.ConventionalExcludes[name] {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		if record, ok := managed[name]; ok && record.Kind == "agent" && sourceWithin(record.Source, sourcePath) && managedFileUnchanged(filepath.Join(targetDir, name), record) {
			continue
		}

		agents = append(agents, LocalAgentInfo{
			Name: name,
			Path: filepath.Join(targetDir, name),
		})
	}

	return agents, nil
}

// PullAgent copies a single local agent file from target to source.
func PullAgent(agent LocalAgentInfo, sourcePath string, force bool) error {
	destPath := filepath.Join(sourcePath, agent.Name)

	if _, err := os.Stat(destPath); err == nil {
		if !force {
			return ErrAlreadyExists
		}
		if err := os.RemoveAll(destPath); err != nil {
			return fmt.Errorf("failed to remove existing: %w", err)
		}
	}

	data, err := os.ReadFile(agent.Path)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", agent.Name, err)
	}
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", agent.Name, err)
	}

	return nil
}

// PullAgents copies multiple local agent files from targets to source.
func PullAgents(agents []LocalAgentInfo, sourcePath string, opts PullOptions) (*PullResult, error) {
	result := &PullResult{
		Failed: make(map[string]error),
	}

	if !opts.DryRun {
		if err := os.MkdirAll(sourcePath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create agent source dir: %w", err)
		}
	}

	for _, agent := range agents {
		if opts.DryRun {
			result.Pulled = append(result.Pulled, agent.Name)
			continue
		}

		err := PullAgent(agent, sourcePath, opts.Force)
		if err != nil {
			if errors.Is(err, ErrAlreadyExists) {
				result.Skipped = append(result.Skipped, agent.Name)
			} else {
				result.Failed[agent.Name] = err
			}
			continue
		}

		result.Pulled = append(result.Pulled, agent.Name)
	}

	return result, nil
}
