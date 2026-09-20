package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"skillshare/internal/config"
	"skillshare/internal/oplog"
	"skillshare/internal/sync"
	"skillshare/internal/ui"
)

// extrasAgentsName is the extras entry name that may overlap with the agents sync system.
const extrasAgentsName = "agents"

type syncExtrasJSONOutput struct {
	Extras   []syncExtrasJSONEntry `json:"extras"`
	Duration string                `json:"duration"`
}

type syncExtrasJSONEntry struct {
	Name    string                 `json:"name"`
	Targets []syncExtrasJSONTarget `json:"targets"`
}

type syncExtrasJSONTarget struct {
	Path      string   `json:"path"`
	Mode      string   `json:"mode"`
	Synced    int      `json:"synced"`
	Skipped   int      `json:"skipped"`
	Pruned    int      `json:"pruned"`
	Error     string   `json:"error,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	SkippedBy string   `json:"skipped_by,omitempty"`
}

func cmdSyncExtras(args []string) error {
	start := time.Now()

	mode, rest, err := parseModeArgs(args)
	if err != nil {
		return err
	}

	dryRun, force, jsonOutput, _ := parseSyncFlags(rest)

	cwd, _ := os.Getwd()
	if mode == modeAuto {
		if projectConfigExists(cwd) {
			mode = modeProject
		} else {
			mode = modeGlobal
		}
	}

	applyModeLabel(mode)

	if mode == modeProject {
		return cmdSyncExtrasProject(cwd, dryRun, force, jsonOutput, start)
	}
	return cmdSyncExtrasGlobal(dryRun, force, jsonOutput, start)
}

func cmdSyncExtrasGlobal(dryRun, force, jsonOutput bool, start time.Time) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !dryRun {
		var output io.Writer = os.Stdout
		if jsonOutput {
			output = io.Discard
		}
		warnings := config.MigrateExtrasDir(filepath.Dir(cfg.EffectiveSkillsSource()), cfg.Extras, output)
		if !jsonOutput {
			for _, warning := range warnings {
				ui.Warning(warning)
			}
		}
	}
	entries := runExtrasSyncEntries(cfg.Extras, func(extra config.ExtraConfig) string {
		return config.ResolveExtrasSourceDir(extra, cfg.EffectiveExtrasSource(), cfg.EffectiveSkillsSource())
	}, dryRun, force, "", collectAgentTargetPathsGlobal(cfg))
	return finishExtrasSync(entries, config.ConfigPath(), "global", dryRun, force, jsonOutput, start)
}

func cmdSyncExtrasProject(cwd string, dryRun, force, jsonOutput bool, start time.Time) error {
	cfg, err := config.LoadProject(cwd)
	if err != nil {
		return err
	}
	entries := runExtrasSyncEntries(cfg.Extras, func(extra config.ExtraConfig) string {
		return config.ExtrasSourceDirProject(cfg.EffectiveExtrasSource(cwd), extra.Name)
	}, dryRun, force, cwd, collectAgentTargetPathsProject(cwd))
	return finishExtrasSync(entries, config.ProjectConfigPath(cwd), "project", dryRun, force, jsonOutput, start)
}

func extrasSyncError(entries []syncExtrasJSONEntry) error {
	var failures []error
	for _, entry := range entries {
		for _, target := range entry.Targets {
			if target.Error != "" {
				failures = append(failures, fmt.Errorf("%s (%s): %s", entry.Name, target.Path, target.Error))
			}
		}
	}
	return errors.Join(failures...)
}

func finishExtrasSync(entries []syncExtrasJSONEntry, configPath, scope string, dryRun, force, jsonOutput bool, start time.Time) error {
	stats := ui.ExtrasSyncStats{Duration: time.Since(start)}
	failed := 0
	if !jsonOutput {
		ui.Header(ui.WithModeLabel("Syncing extras"))
		if dryRun {
			ui.Warning("Dry run mode - no changes will be made")
		}
		if len(entries) == 0 {
			ui.Info("No extras configured.")
		}
	}
	for _, entry := range entries {
		for _, target := range entry.Targets {
			stats.Targets++
			stats.Synced += target.Synced
			stats.Skipped += target.Skipped
			stats.Pruned += target.Pruned
			if target.Error != "" {
				failed++
			}
			if jsonOutput {
				continue
			}
			path := shortenPath(target.Path)
			switch {
			case target.Error != "":
				ui.Warning("%s: %s", path, target.Error)
			case target.SkippedBy != "":
				ui.Warning("Skipping extras %q target %s: already managed by %s sync", entry.Name, path, target.SkippedBy)
			case target.Skipped > 0:
				ui.Warning("%s: %d synced, %d skipped, %d pruned (use --force to override conflicts)", path, target.Synced, target.Skipped, target.Pruned)
			default:
				ui.Success("%s: %d files %s, %d pruned (%s)", path, target.Synced, syncVerb(target.Mode), target.Pruned, target.Mode)
			}
			for _, warning := range target.Warnings {
				ui.Warning("%s", warning)
			}
		}
	}
	err := extrasSyncError(entries)
	if !dryRun {
		status := "ok"
		if err != nil {
			status = "partial"
		}
		entry := oplog.NewEntry("sync-extras", status, time.Since(start))
		entry.Args = map[string]any{"extras_count": len(entries), "synced": stats.Synced, "skipped": stats.Skipped, "pruned": stats.Pruned, "errors": failed, "dry_run": dryRun, "force": force, "scope": scope}
		_ = oplog.WriteWithLimit(configPath, oplog.OpsFile, entry, logMaxEntries())
	}
	if jsonOutput {
		return writeJSONResult(&syncExtrasJSONOutput{Extras: entries, Duration: formatDuration(start)}, err)
	}
	ui.ExtrasSyncSummary(stats)
	return err
}

// syncVerb returns a user-facing verb for the given sync mode.
func syncVerb(mode string) string {
	switch mode {
	case "copy":
		return "copied"
	case "symlink":
		return "linked"
	default:
		return "synced"
	}
}

// runExtrasSync runs extras sync and returns JSON entries without printing.
// Used by sync --all --json to merge extras into the skills JSON output.
// agentTargetPaths is used to skip extras "agents" targets that overlap with the agents sync system.
func runExtrasSyncEntries(extras []config.ExtraConfig, sourceFunc func(config.ExtraConfig) string, dryRun, force bool, projectRoot string, agentTargetPaths map[string]bool) []syncExtrasJSONEntry {
	entries := make([]syncExtrasJSONEntry, 0, len(extras))
	for _, extra := range extras {
		extraSource := sourceFunc(extra)
		entry := syncExtrasJSONEntry{Name: extra.Name}

		for _, target := range extra.Targets {
			mode := target.Mode
			if mode == "" {
				mode = "merge"
			}
			targetPath := config.ExpandPath(target.Path)
			if projectRoot != "" {
				targetPath = resolveProjectPath(projectRoot, target.Path)
			}

			if extra.Name == extrasAgentsName && isExtrasTargetOverlappingAgents(targetPath, agentTargetPaths) {
				entry.Targets = append(entry.Targets, syncExtrasJSONTarget{
					Path: targetPath, Mode: mode, SkippedBy: extrasAgentsName,
				})
				continue
			}

			// Resolve the per-target transform extension so --all --json applies
			// it like a normal sync instead of copying files verbatim.
			var spec *sync.ExtensionSpec
			if target.Extension != "" {
				effMode, modeErr := validateExtensionMode(target.Mode)
				if modeErr != nil {
					entry.Targets = append(entry.Targets, syncExtrasJSONTarget{
						Path: targetPath, Mode: mode, Error: modeErr.Error(),
					})
					continue
				}
				mode = effMode
				extDir := globalExtensionsDir()
				if projectRoot != "" {
					extDir = projectExtensionsDir(projectRoot)
				}
				var specErr error
				spec, specErr = resolveExtension(target.Extension, extDir)
				if specErr != nil {
					entry.Targets = append(entry.Targets, syncExtrasJSONTarget{
						Path: targetPath, Mode: mode, Error: specErr.Error(),
					})
					continue
				}
			}

			result, syncErr := sync.SyncExtra(extraSource, targetPath, mode, dryRun, force, target.Flatten, projectRoot, spec)
			jt := syncExtrasJSONTarget{Path: targetPath, Mode: mode}
			if syncErr != nil {
				jt.Error = syncErr.Error()
			} else {
				jt.Synced = result.Synced
				jt.Skipped = result.Skipped
				jt.Pruned = result.Pruned
				jt.Warnings = result.Warnings
				if len(result.Errors) > 0 {
					jt.Error = strings.Join(result.Errors, "; ")
				}
			}
			entry.Targets = append(entry.Targets, jt)
		}

		entries = append(entries, entry)
	}
	return entries
}

// isExtrasTargetOverlappingAgents checks whether an extras target path overlaps
// with any active agent target path.
func isExtrasTargetOverlappingAgents(targetPath string, agentPaths map[string]bool) bool {
	if len(agentPaths) == 0 {
		return false
	}
	return agentPaths[filepath.Clean(targetPath)]
}

// cachedHome caches the home directory for shortenPath.
var cachedHome = func() string {
	h, _ := os.UserHomeDir()
	return h
}()

// shortenPath replaces the home directory prefix with ~.
func shortenPath(p string) string {
	if cachedHome != "" && strings.HasPrefix(p, cachedHome) {
		return "~" + p[len(cachedHome):]
	}
	return p
}
