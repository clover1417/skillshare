package sync

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"skillshare/internal/utils"
)

// ExtraResult holds the result of an extras sync operation.
type ExtraResult struct {
	Synced   int      // Files synced (new + already correct)
	Skipped  int      // Files skipped (local conflict, no --force)
	Pruned   int      // Orphan files removed
	Errors   []string // Non-fatal error messages
	Warnings []string // Non-fatal warnings (e.g. flatten collisions)
}

// reservedMetadataFile is skillshare's per-directory install-tracking store
// (mirrors install.MetadataFileName, duplicated here to avoid an import cycle).
// It is internal bookkeeping, not user content, so it must never be synced or
// transformed into a target directory.
const reservedMetadataFile = ".metadata.json"

// DiscoverExtraFiles recursively walks sourcePath and returns relative paths
// of all regular files. Directories named ".git" and skillshare's reserved
// .metadata.json bookkeeping file are skipped. Results are sorted for
// deterministic output.
func DiscoverExtraFiles(sourcePath string) ([]string, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("extras source directory does not exist: %s", sourcePath)
		}
		return nil, fmt.Errorf("failed to stat extras source: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("extras source is not a directory: %s", sourcePath)
	}

	var files []string
	err = filepath.Walk(sourcePath, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fi.IsDir() {
			if fi.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if fi.Name() == reservedMetadataFile || fi.Name() == fileManifestName || fi.Name() == fileManifestName+".lock" || strings.HasPrefix(fi.Name(), ".skillshare-write-") {
			return nil // skillshare's internal tracking store, never sync it
		}
		rel, relErr := filepath.Rel(sourcePath, path)
		if relErr != nil {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk extras source: %w", err)
	}

	sort.Strings(files)
	return files, nil
}

// SyncExtra synchronises extra files from sourcePath into targetPath.
//
// Supported modes:
//   - "merge" (default): per-file symlink from target to source
//   - "copy":            per-file copy
//   - "symlink":         entire directory symlink
//
// When spec is non-nil, the sync is routed through syncExtraTransform
// (transform/extension mode). When spec is nil, behavior is unchanged.
// When dryRun is true the function counts what would happen but makes no
// filesystem changes.
func SyncExtra(sourcePath, targetPath, mode string, dryRun, force, flatten bool, projectRoot string, spec *ExtensionSpec) (*ExtraResult, error) {
	if spec != nil {
		// Transform extensions emit generated files into the target, so only
		// copy semantics apply. Reject merge/symlink here so the API and the
		// CLI (validateExtensionMode) enforce the same contract.
		if mode != "" && mode != "copy" {
			return nil, fmt.Errorf("extension %q requires copy mode, got %q", spec.Name, mode)
		}
		return syncExtraTransform(sourcePath, targetPath, spec, dryRun, force, flatten)
	}
	if mode == "" {
		mode = "merge"
	}
	if flatten && mode == "symlink" {
		return nil, fmt.Errorf("flatten cannot be used with symlink mode")
	}

	switch mode {
	case "symlink":
		return syncExtraSymlinkMode(sourcePath, targetPath, dryRun, force, projectRoot)
	case "merge", "copy":
		return syncExtraPerFile(sourcePath, targetPath, mode, dryRun, force, flatten, projectRoot)
	default:
		return nil, fmt.Errorf("unsupported extras sync mode: %q", mode)
	}
}

// syncExtraTransform applies an extension to every source file and writes the
// transformed output into targetPath using copy semantics. Output files are
// renamed per spec.OutputExt.
func syncExtraTransform(sourcePath, targetPath string, spec *ExtensionSpec, dryRun, force, flatten bool) (*ExtraResult, error) {
	result := &ExtraResult{}

	files, err := DiscoverExtraFiles(sourcePath)
	if err != nil {
		return nil, err
	}
	absSrc, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve source path: %w", err)
	}

	seen := make(map[string]string) // flatten: basename → original rel
	for _, rel := range files {
		srcFile := filepath.Join(absSrc, rel)
		tgtRel := rel
		if flatten {
			base := filepath.Base(rel)
			if prev, ok := seen[base]; ok {
				result.Skipped++
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("flatten conflict: %s skipped (%s already synced from %s)", rel, base, prev))
				continue
			}
			seen[base] = rel
			tgtRel = base
		}
		tgtRel = ApplyOutputExt(tgtRel, spec.OutputExt)
		tgtFile := filepath.Join(targetPath, tgtRel)

		// Dry-run reports the outcome without spawning the extension and without
		// touching the filesystem. The transformed output can only be produced by
		// a subprocess, and a preview must never execute arbitrary extension code,
		// so content can't be compared here. An existing real file or directory is
		// therefore conservatively reported as a conflict requiring --force —
		// matching the non-dry-run skip path below — while a missing target, a
		// leftover symlink, or --force would be (re)written and counts as synced.
		if dryRun {
			if info, lstatErr := os.Lstat(tgtFile); lstatErr == nil &&
				info.Mode()&os.ModeSymlink == 0 && !force {
				result.Skipped++
			} else {
				result.Synced++
			}
			continue
		}

		env := map[string]string{
			"SS_SRC_PATH":   srcFile,
			"SS_REL_PATH":   rel,
			"SS_TARGET_DIR": targetPath,
			"SS_MODE":       "sync",
		}

		out, runErr := runExtension(spec, srcFile, env)
		if runErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", rel, runErr))
			continue
		}

		if info, err := os.Lstat(tgtFile); err == nil && info.IsDir() && !utils.IsSymlinkOrJunction(tgtFile) && force {
			if err := os.RemoveAll(tgtFile); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", rel, err))
				continue
			}
		}
		synced, skipped, err := syncManagedContent(srcFile, tgtFile, out, 0644, "copy", false, force, false, "extra")
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		result.Synced += synced
		result.Skipped += skipped

	}

	return result, nil
}

// syncExtraSymlinkMode symlinks the entire source directory to the target path.
func syncExtraSymlinkMode(sourcePath, targetPath string, dryRun, force bool, projectRoot string) (*ExtraResult, error) {
	if err := validateDirectoryLink(sourcePath, targetPath); err != nil {
		return nil, err
	}
	result := &ExtraResult{}

	absSrc, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve source path: %w", err)
	}

	// Check existing target
	_, lstatErr := os.Lstat(targetPath)
	if lstatErr == nil {
		// Something exists at targetPath
		if utils.IsSymlinkOrJunction(targetPath) {
			// Already a symlink — check if correct
			dest, readErr := os.Readlink(targetPath)
			if readErr == nil {
				absDest := resolveReadlink(dest, targetPath)
				if absDest == absSrc {
					relative := shouldUseRelative(projectRoot, absSrc, targetPath)
					if !linkNeedsReformat(dest, relative) {
						result.Synced = 1
						return result, nil
					}
					// Correct target but wrong format (abs↔rel) — recreate
					if dryRun {
						result.Synced = 1
						return result, nil
					}
					if err := reformatLink(targetPath, absSrc, relative); err != nil {
						return nil, fmt.Errorf("failed to reformat directory symlink: %w", err)
					}
					result.Synced = 1
					return result, nil
				}
			}
			// Wrong symlink
			if !force {
				result.Skipped = 1
				return result, nil
			}
			if !dryRun {
				if err := os.Remove(targetPath); err != nil {
					return nil, fmt.Errorf("failed to remove wrong symlink: %w", err)
				}
			}
		} else {
			// Real file/dir
			if !force {
				result.Skipped = 1
				return result, nil
			}
			if !dryRun {
				os.RemoveAll(targetPath)
			}
		}
	}

	if dryRun {
		result.Synced = 1
		return result, nil
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create parent directory: %w", err)
	}
	relative := shouldUseRelative(projectRoot, absSrc, targetPath)
	if err := createLink(targetPath, absSrc, relative); err != nil {
		return nil, fmt.Errorf("failed to create directory symlink: %w", err)
	}
	result.Synced = 1
	return result, nil
}

// syncExtraPerFile handles merge (symlink) and copy modes on a per-file basis.
func syncExtraPerFile(sourcePath, targetPath, mode string, dryRun, force, flatten bool, projectRoot string) (*ExtraResult, error) {
	result := &ExtraResult{}

	files, err := DiscoverExtraFiles(sourcePath)
	if err != nil {
		return nil, err
	}

	absSrc, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve source path: %w", err)
	}

	seen := make(map[string]string) // basename → original rel path (flatten only)

	// Compute once for entire batch — all files share the same source/target root.
	relative := shouldUseRelative(projectRoot, sourcePath, targetPath)

	for _, rel := range files {
		srcFile := filepath.Join(absSrc, rel)
		tgtRel := rel
		if flatten {
			base := filepath.Base(rel)
			if prev, exists := seen[base]; exists {
				result.Skipped++
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("flatten conflict: %s skipped (%s already synced from %s)", rel, base, prev))
				continue
			}
			seen[base] = rel
			tgtRel = base
		}
		tgtFile := filepath.Join(targetPath, tgtRel)

		synced, skipped, syncErr := syncOneExtraFile(srcFile, tgtFile, mode, dryRun, force, relative)
		if syncErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", rel, syncErr))
			continue
		}
		result.Synced += synced
		result.Skipped += skipped
	}

	if !dryRun && len(result.Errors) == 0 {
		sourceSet := make(map[string]bool, len(files))
		if flatten {
			for base := range seen {
				sourceSet[base] = true
			}
		} else {
			for _, f := range files {
				sourceSet[f] = true
			}
		}
		pruned, pruneErrors := pruneOwnedExtras(absSrc, targetPath, sourceSet)
		result.Pruned = pruned
		result.Errors = append(result.Errors, pruneErrors...)
	}

	return result, nil
}

// syncOneExtraFile syncs a single file. Returns (synced, skipped, error).
func syncOneExtraFile(srcFile, tgtFile, mode string, dryRun, force, relative bool) (int, int, error) {
	return syncManagedFile(srcFile, tgtFile, mode, dryRun, force, relative, "extra")
}

func PruneExtraTarget(targetPath, mode string) (int, []string) {
	if mode == "symlink" {
		return pruneExtraSymlinkTarget(targetPath)
	}
	if mode != "" && mode != "merge" && mode != "copy" {
		return 0, []string{fmt.Sprintf("unsupported extras sync mode: %q", mode)}
	}
	return pruneExtraManifests(targetPath, func(string, managedFile) bool { return true }, nil)
}

func PruneExtraTargetFiles(targetPath, mode string, managedFiles map[string]bool) (int, []string) {
	switch mode {
	case "", "merge", "copy":
		return pruneExtraManifests(targetPath, func(rel string, entry managedFile) bool { return managedFiles[rel] }, managedFiles)
	case "symlink":
		return pruneExtraSymlinkTarget(targetPath)
	default:
		return 0, []string{fmt.Sprintf("unsupported extras sync mode: %q", mode)}
	}
}

func pruneExtraSymlinkTarget(targetPath string) (pruned int, errors []string) {
	_, err := os.Lstat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, []string{fmt.Sprintf("prune %s: %v", targetPath, err)}
	}
	if !utils.IsSymlinkOrJunction(targetPath) {
		return 0, []string{fmt.Sprintf("prune %s: target is not a symlink", targetPath)}
	}
	if err := os.Remove(targetPath); err != nil {
		return 0, []string{fmt.Sprintf("prune %s: %v", targetPath, err)}
	}
	return 1, nil
}

// contentEqual returns true if two files have identical content.
func contentEqual(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()

	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()

	bufA := make([]byte, 4096)
	bufB := make([]byte, 4096)
	for {
		nA, errA := fa.Read(bufA)
		nB, errB := fb.Read(bufB)
		if !bytes.Equal(bufA[:nA], bufB[:nB]) {
			return false
		}
		if errA == io.EOF && errB == io.EOF {
			return true
		}
		if errA != nil || errB != nil {
			return false
		}
	}
}

// EffectiveMode returns the mode to use for sync, defaulting to "merge".
func EffectiveMode(mode string) string {
	if mode == "" {
		return "merge"
	}
	return mode
}

// FlattenRel returns the target-relative path for a source file under flatten mode.
// When flatten is true, it uses only the basename and tracks seen basenames to skip
// collisions (first-wins, matching sync behavior). Returns ("", false) for collisions.
func FlattenRel(rel string, flatten bool, seen map[string]bool) (tgtRel string, ok bool) {
	if !flatten {
		return rel, true
	}
	base := filepath.Base(rel)
	if seen[base] {
		return "", false
	}
	seen[base] = true
	return base, true
}

// CheckSyncStatus compares source files against the target directory and
// returns a status string: "synced" or "drift". When outputExt is non-empty a
// transform extension is in effect, so the expected target file carries the
// transformed extension (e.g. foo.md → foo.toml) instead of the source name.
func CheckSyncStatus(sourceFiles []string, sourceDir, targetDir, mode string, flatten bool, outputExt string) string {
	if mode == "symlink" {
		resolved, err := utils.ResolveLinkTarget(targetDir)
		info, statErr := os.Stat(targetDir)
		if err == nil && statErr == nil && info.IsDir() && linkResolvesToSource(resolved, sourceDir) {
			return "synced"
		}
		return "drift"
	}
	mode = effectiveFileMode(mode)
	seen := make(map[string]bool)
	for _, rel := range sourceFiles {
		tgtRel, ok := FlattenRel(rel, flatten, seen)
		if !ok {
			continue
		}
		tgtRel = ApplyOutputExt(tgtRel, outputExt)
		targetFile := filepath.Join(targetDir, tgtRel)
		sourceFile := filepath.Join(sourceDir, rel)

		tInfo, err := os.Lstat(targetFile)
		if err != nil {
			return "drift"
		}

		switch mode {
		case "symlink", "merge":
			if tInfo.Mode()&os.ModeSymlink != 0 {
				link, readErr := os.Readlink(targetFile)
				if readErr != nil || filepath.Clean(resolveReadlink(link, targetFile)) != filepath.Clean(sourceFile) {
					return "drift"
				}
			} else {
				return "drift"
			}
		case "copy":
			if !tInfo.Mode().IsRegular() {
				return "drift"
			}
			if outputExt == "" && !contentEqual(sourceFile, targetFile) {
				return "drift"
			}
		}
	}
	return "synced"
}

// ExtraCollectResult holds results from collecting extra files.
type ExtraCollectResult struct {
	Collected int
	Skipped   int
	Errors    []string
}

// CollectExtraFiles scans targetDir for non-symlink local files,
// copies them to sourceDir, and replaces originals with symlinks.
// When flatten is true, collected files are placed in the source root
// (basename only) rather than preserving the target subdirectory structure.
func CollectExtraFiles(sourceDir, targetDir string, dryRun, flatten bool, projectRoot string) (*ExtraCollectResult, error) {
	result := &ExtraCollectResult{}

	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("target directory does not exist: %s", targetDir)
	}

	// Ensure source directory exists
	if !dryRun {
		if err := os.MkdirAll(sourceDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create source directory: %w", err)
		}
	}

	err := filepath.Walk(targetDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		// Skip directories and .git
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		// filepath.Walk follows symlinks, so check via Lstat to detect them
		linfo, err := os.Lstat(path)
		if err != nil {
			return nil
		}
		if utils.IsSymlinkOrJunction(path) || linfo.Name() == fileManifestName || linfo.Name() == fileManifestName+".lock" {
			result.Skipped++
			return nil
		}

		rel, err := filepath.Rel(targetDir, path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("cannot compute relative path: %s", path))
			return nil
		}

		destRel := rel
		if flatten {
			destRel = filepath.Base(rel)
		}
		destPath := filepath.Join(sourceDir, destRel)

		// Skip if already exists in source
		if _, err := os.Stat(destPath); err == nil {
			result.Skipped++
			return nil
		}

		if dryRun {
			result.Collected++
			return nil
		}

		// Create parent directory
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("mkdir failed: %v", err))
			return nil
		}

		// Read source content
		content, err := os.ReadFile(path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("read failed: %v", err))
			return nil
		}

		// Write to source dir
		if err := os.WriteFile(destPath, content, info.Mode().Perm()); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("write failed: %v", err))
			return nil
		}

		relative := shouldUseRelative(projectRoot, destPath, path)
		if _, _, err := syncManagedFile(destPath, path, "merge", false, true, relative, "extra"); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("link collected file: %v", err))
			return nil
		}

		result.Collected++
		return nil
	})

	if err != nil {
		return result, fmt.Errorf("walk error: %w", err)
	}

	return result, nil
}
