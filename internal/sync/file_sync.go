package sync

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	"skillshare/internal/utils"
)

const fileManifestName = ".skillshare-files.json"

type managedFile struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Hash   string `json:"hash"`
}

func readFileManifest(dir string) (map[string]managedFile, error) {
	entries := make(map[string]managedFile)
	data, err := os.ReadFile(filepath.Join(dir, fileManifestName))
	if os.IsNotExist(err) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("invalid file sync manifest: %w", err)
	}
	if entries == nil {
		entries = make(map[string]managedFile)
	}
	return entries, nil
}

func writeFileManifest(dir string, entries map[string]managedFile) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return replaceFile(filepath.Join(dir, fileManifestName), data, 0600)
}

func replaceFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".skillshare-write-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if utils.IsSymlinkOrJunction(path) {
		if info, err := os.Lstat(path); err == nil && (runtime.GOOS == "windows" || info.IsDir()) {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return os.Rename(name, path)
}

func effectiveFileMode(mode string) string {
	if (mode == "" || mode == "merge") && runtime.GOOS == "windows" && !canCreateRelativeLink() {
		return "copy"
	}
	return EffectiveMode(mode)
}

func syncManagedFile(srcFile, tgtFile, mode string, dryRun, force, relative bool, kind string) (int, int, error) {
	srcFile, err := filepath.Abs(srcFile)
	if err != nil {
		return 0, 0, err
	}
	if utils.PathsEqual(evalOrClean(srcFile), evalOrClean(tgtFile)) && !utils.IsSymlinkOrJunction(tgtFile) {
		return 0, 0, fmt.Errorf("source and target are the same file: %s", tgtFile)
	}
	sourceInfo, err := os.Stat(srcFile)
	if err != nil {
		return 0, 0, err
	}
	if !sourceInfo.Mode().IsRegular() {
		return 0, 0, fmt.Errorf("source is not a regular file: %s", srcFile)
	}
	data, err := os.ReadFile(srcFile)
	if err != nil {
		return 0, 0, err
	}
	return syncManagedContent(srcFile, tgtFile, data, sourceInfo.Mode().Perm(), mode, dryRun, force, relative, kind)
}

func syncManagedContent(srcFile, tgtFile string, data []byte, permissions os.FileMode, mode string, dryRun, force, relative bool, kind string) (int, int, error) {
	if utils.PathsEqual(evalOrClean(srcFile), evalOrClean(tgtFile)) && !utils.IsSymlinkOrJunction(tgtFile) {
		return 0, 0, fmt.Errorf("source and target are the same file: %s", tgtFile)
	}
	mode = effectiveFileMode(mode)
	dir := filepath.Dir(tgtFile)
	if !dryRun {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return 0, 0, err
		}
		lock := flock.New(filepath.Join(dir, fileManifestName+".lock"))
		if err := lock.Lock(); err != nil {
			return 0, 0, err
		}
		defer lock.Close()
	}
	entries, err := readFileManifest(dir)
	if err != nil {
		return 0, 0, err
	}
	name := filepath.Base(tgtFile)
	previous, owned := entries[name]
	owned = owned && linkResolvesToSource(previous.Source, srcFile)
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	record := managedFile{Kind: kind, Source: srcFile, Hash: hash}
	info, statErr := os.Lstat(tgtFile)
	if statErr != nil && !os.IsNotExist(statErr) {
		return 0, 0, statErr
	}
	if statErr == nil {
		isLink := utils.IsSymlinkOrJunction(tgtFile)
		if mode == "merge" && isLink && info.Mode()&os.ModeSymlink != 0 {
			dest, err := os.Readlink(tgtFile)
			targetInfo, targetErr := os.Stat(tgtFile)
			if err == nil && targetErr == nil && targetInfo.Mode().IsRegular() && linkResolvesToSource(resolveReadlink(dest, tgtFile), srcFile) && !linkNeedsReformat(dest, relative) {
				if !dryRun {
					entries[name] = record
					if err := writeFileManifest(dir, entries); err != nil {
						return 0, 0, err
					}
				}
				return 1, 0, nil
			}
		}
		if !isLink {
			if info.IsDir() {
				return 0, 1, nil
			}
			current, err := utils.FileHash(tgtFile)
			if err != nil {
				return 0, 0, err
			}
			if mode == "copy" && current == hash {
				if (owned || force) && !dryRun {
					entries[name] = record
					if err := writeFileManifest(dir, entries); err != nil {
						return 0, 0, err
					}
				}
				return 1, 0, nil
			}
			if !force && (!owned || current != previous.Hash) {
				return 0, 1, nil
			}
		}
	}
	if dryRun {
		return 1, 0, nil
	}
	if mode == "copy" {
		if err := replaceFile(tgtFile, data, permissions); err != nil {
			return 0, 0, err
		}
	} else {
		if err := reformatLink(tgtFile, srcFile, relative); err != nil {
			return 0, 0, err
		}
	}
	entries[name] = record
	if err := writeFileManifest(dir, entries); err != nil {
		return 0, 0, err
	}
	return 1, 0, nil
}

func managedFileUnchanged(path string, entry managedFile) bool {
	if utils.IsSymlinkOrJunction(path) {
		dest, err := utils.ResolveLinkTarget(path)
		return err == nil && linkResolvesToSource(dest, entry.Source)
	}
	hash, err := utils.FileHash(path)
	return err == nil && hash == entry.Hash
}

func AgentFileState(target, source string) string {
	entries, err := readFileManifest(filepath.Dir(target))
	if err != nil {
		return "conflict"
	}
	entry, owned := entries[filepath.Base(target)]
	if !owned || entry.Kind != "agent" || (source != "" && !linkResolvesToSource(entry.Source, source)) {
		return ""
	}
	if !managedFileUnchanged(target, entry) {
		return "conflict"
	}
	if source == "" {
		source = entry.Source
	}
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return "orphan"
	}
	if contentEqual(source, target) {
		return "synced"
	}
	return "update"
}

func fileInSync(source, target, mode string, relative bool) bool {
	if effectiveFileMode(mode) == "copy" {
		return !utils.IsSymlinkOrJunction(target) && contentEqual(source, target)
	}
	dest, err := os.Readlink(target)
	return err == nil && linkResolvesToSource(resolveReadlink(dest, target), source) && !linkNeedsReformat(dest, relative)
}

func sourceWithin(path, root string) bool {
	path, pathErr := filepath.Abs(path)
	root, rootErr := filepath.Abs(root)
	if pathErr != nil || rootErr != nil {
		return false
	}
	rel, err := filepath.Rel(evalOrClean(root), evalOrClean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func validateDirectoryLink(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("directory link source: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("directory link source is not a directory: %s", source)
	}
	if !utils.IsSymlinkOrJunction(target) && (sourceWithin(source, target) || sourceWithin(target, source)) {
		return fmt.Errorf("source and target directories overlap: %s, %s", source, target)
	}
	return nil
}

func pruneManagedFiles(dir string, candidate func(string, managedFile) bool, dryRun bool) ([]string, error) {
	if _, err := os.Stat(filepath.Join(dir, fileManifestName)); os.IsNotExist(err) {
		return nil, nil
	}
	if !dryRun {
		lock := flock.New(filepath.Join(dir, fileManifestName+".lock"))
		if err := lock.Lock(); err != nil {
			return nil, err
		}
		defer lock.Close()
	}
	entries, err := readFileManifest(dir)
	if err != nil {
		return nil, err
	}
	var names, removed []string
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := entries[name]
		if name == "." || name == ".." || filepath.Base(name) != name || !candidate(name, entry) {
			continue
		}
		path := filepath.Join(dir, name)
		if !managedFileUnchanged(path, entry) {
			continue
		}
		if !dryRun {
			if err := os.Remove(path); err != nil {
				return removed, err
			}
			delete(entries, name)
		}
		removed = append(removed, name)
	}
	if !dryRun && len(removed) > 0 {
		if err := writeFileManifest(dir, entries); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func pruneOwnedExtras(source, target string, expected map[string]bool) (int, []string) {
	return pruneExtraManifests(target, func(rel string, entry managedFile) bool {
		return sourceWithin(entry.Source, source) && !expected[rel]
	}, expected)
}

func pruneExtraManifests(target string, candidate func(string, managedFile) bool, protected map[string]bool) (int, []string) {
	var dirs []string
	var failures []string
	err := filepath.Walk(target, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if !os.IsNotExist(err) {
				failures = append(failures, err.Error())
			}
			return nil
		}
		if info.IsDir() {
			rel, _ := filepath.Rel(target, path)
			if info.Name() == ".git" || utils.IsSymlinkOrJunction(path) || protected[rel] {
				return filepath.SkipDir
			}
		} else if info.Name() == fileManifestName {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		failures = append(failures, err.Error())
	}
	pruned := 0
	for _, dir := range dirs {
		removed, err := pruneManagedFiles(dir, func(name string, entry managedFile) bool {
			rel, err := filepath.Rel(target, filepath.Join(dir, name))
			return err == nil && entry.Kind == "extra" && candidate(rel, entry)
		}, false)
		pruned += len(removed)
		if err != nil {
			failures = append(failures, err.Error())
		}
	}
	return pruned, failures
}
