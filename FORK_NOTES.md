# Skillshare fork audit

Fork: https://github.com/clover1417/skillshare

Branch: `codex/windows-sync-reliability`

Base: upstream `4415c7a75a308c733d02b53f45d135e5f24776bf`, checked on 2026-09-20.

## Fixed behavior

| Problem reproduced | Result after the patch |
| --- | --- |
| Windows created directory junctions for instruction and agent files. Opening them failed. | File links use real symlinks when permitted. Otherwise merge mode uses tracked copies. Existing broken file junctions are repaired. Directory junctions remain available for skills. |
| Changed extras copies required `--force` for every update. | A SHA-256 manifest records the source and last written content. Unchanged managed copies refresh normally, including after an editor replaces the source file by rename. |
| Agent copy sync overwrote independent local agents. | Local conflicts and local edits are preserved unless explicitly forced. |
| Agent pruning removed every unexpected Markdown file. Extras pruning could remove unrelated links. | Pruning requires ownership metadata and unchanged managed content. Resource kind and extras source are checked. Target removal also preserves local edits and independent files. |
| File synchronization implementations differed between agents, extras and transformed output. | Shared file writing and ownership handling, with a per-directory lock and temporary-file replacement. Metadata is excluded from extra collection/discovery. |
| `sync extras --json` returned exit code zero despite errors. `sync --all` discarded extras errors. | JSON stays parseable and failures return nonzero status. Text, JSON, TUI and HTTP callers propagate resource failures. |
| Dry runs created or moved extras sources and removed empty directories. | Extras previews preserve those directories. Missing sources are reported as errors rather than recreated and treated as empty. |
| MCP could be applied before extras failed, including in project mode. | MCP preflight runs before resource synchronization; MCP apply runs after successful resource synchronization. |
| Root `pull` omitted extras and MCP. Dashboard pull skipped target repair when Git was up to date. | Root pull processes skills, agents, extras and MCP, including target repair without new commits. HTTP failures identify that Git completed but synchronization failed. |
| Agent diff/doctor treated managed Windows copies as independent local files. | Managed copies are recognized and compared against their source. |
| Forced directory-link sync could remove an overlapping source directory. | Missing sources and overlapping real source/target directories are rejected before replacement. Directory-junction status is recognized. |

## Verification

[Passing Linux and Windows CI](https://github.com/clover1417/skillshare/actions/runs/35489284194), code commit `b7e81872`:

- Linux: formatting, `go vet ./...`, `go test -race ./internal/... ./cmd/skillshare`, binary build, and the complete integration suite with the race detector.
- Windows runner: vet, targeted regression tests, binary build and `scripts/test_windows_sync.ps1`.
- Local Windows without Developer Mode: targeted regressions and the native CLI script, with all 26 assertions passing.
- UI: `npm run build` passed. The running dashboard displayed the injected extras error, then successful synchronization after the fixture was repaired. No browser console errors were reported.

The native script checks shared/private skills and MCP, local skills and agents, readable instruction files, source replacement, local conflicts, explicit force, root pull repair, JSON errors, and preservation of native Claude/Codex settings. All client data is synthetic and stored in isolated temporary directories.

The unmodified upstream suite assumes Unix paths, symlink permissions and shell helpers in multiple Windows tests; one command test also waited for TUI input until its timeout. The full suite was validated on Linux. Windows coverage is the dedicated regression and native workflow suite, not a claim that every upstream test is portable.

## Running the fork

Use this branch when cloning:

```powershell
git clone --branch codex/windows-sync-reliability https://github.com/clover1417/skillshare.git
cd skillshare
go build -o bin/skillshare.exe ./cmd/skillshare
./scripts/test_windows_sync.ps1 -Binary ./bin/skillshare.exe
```

The working checkout on this machine contains `bin/skillshare.exe`, version `0.21.1-fork.b7e81872`. Its matching web assets were built from the same checkout and cached locally. A normal source build uses version `dev`; follow the upstream UI development/build workflow when rebuilding the frontend on another machine.

## Operational boundaries

- On Windows without symlink privileges, changing a source instruction/agent file requires another sync to refresh its managed copy. Editing the target creates a preserved local conflict.
- Ownership state lives in `.skillshare-files.json` beside the synchronized files, with a companion lock file. Keep these files. Legacy untracked copies and orphan links are preserved conservatively. Explicit `--force` adopts a conflicting copy after review.
- Missing extras directories now produce an error. Create them through initialization or restore the source before syncing.
- A resource failure can leave earlier resource targets synchronized. The operation is not a transaction across every resource; MCP remains unapplied after such a failure.
- This remains a source-to-target synchronizer with explicit collection/import commands. Automatic two-way reconciliation was not added.
- Git root synchronization still intentionally excludes machine-local `config.yaml`. Use an external `sources.mcp` YAML and a reviewed, secret-free config template for another machine. Environment variables and client credentials remain machine-specific.
- Upstream self-upgrade may replace this fork. Rebuild this branch or port the patches before upgrading.
- Existing selectors, TUI motion and dashboard design are retained. This work concentrates on synchronization correctness, error reporting and related cleanup.
