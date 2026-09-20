param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [string]$FixtureRoot = (Join-Path ([IO.Path]::GetTempPath()) ('skillshare-native-' + [guid]::NewGuid()))
)

$ErrorActionPreference = 'Stop'
$Binary = (Resolve-Path -LiteralPath $Binary).Path
$fixture = [IO.Path]::GetFullPath($FixtureRoot)
$sourceRoot = Join-Path $fixture 'config\skillshare'
$clientRoot = Join-Path $fixture 'clients'
$checks = [Collections.Generic.List[string]]::new()

function Write-Fixture([string]$Path, [string]$Content) {
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($Path)) | Out-Null
    [IO.File]::WriteAllText($Path, $Content, [Text.UTF8Encoding]::new($false))
}

function Require([bool]$Condition, [string]$Label) {
    if (-not $Condition) { throw $Label }
    $checks.Add($Label)
}

function Invoke-Fixture([string[]]$Arguments, [string]$Name, [bool]$ExpectFailure = $false) {
    $info = [Diagnostics.ProcessStartInfo]::new($Binary)
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.WorkingDirectory = $fixture
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($arg in $Arguments) { $info.ArgumentList.Add($arg) }
    $info.Environment['SKILLSHARE_CONFIG'] = "$sourceRoot\config.yaml"
    $info.Environment['HOME'] = $clientRoot
    $info.Environment['USERPROFILE'] = $clientRoot
    $info.Environment['APPDATA'] = "$fixture\appdata"
    $info.Environment['CLAUDE_CONFIG_DIR'] = "$clientRoot\.claude"
    $info.Environment['CODEX_HOME'] = "$clientRoot\.codex"
    foreach ($kind in @('CONFIG', 'DATA', 'STATE', 'CACHE')) {
        $info.Environment["XDG_${kind}_HOME"] = "$fixture\$($kind.ToLower())"
    }
    $process = [Diagnostics.Process]::Start($info)
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(30000)) {
        $process.Kill($true)
        throw "Timed out: $Name"
    }
    $result = @{ exit = $process.ExitCode; stdout = $stdout.Result; stderr = $stderr.Result; args = $Arguments }
    Write-Fixture "$fixture\logs\$Name.json" ($result | ConvertTo-Json -Depth 8)
    Require (($result.exit -ne 0) -eq $ExpectFailure) "$Name exit status"
    return $result
}

foreach ($name in @('shared', 'claude-only', 'codex-only')) {
    Write-Fixture "$sourceRoot\skills\$name\SKILL.md" "---`nname: $name`ndescription: Native smoke fixture.`n---`nShared instructions.`n"
}
Write-Fixture "$sourceRoot\extras\instructions\AGENTS.md" 'shared v1'
Write-Fixture "$sourceRoot\agents\reviewer.md" '# Reviewer'
Write-Fixture "$clientRoot\.claude\skills\local-private\SKILL.md" "---`nname: local-private`ndescription: Private fixture.`n---`nKeep local.`n"
Write-Fixture "$clientRoot\.claude\agents\private.md" '# Private'
Write-Fixture "$clientRoot\.claude\CLAUDE.md" '@AGENTS.md'
Write-Fixture "$clientRoot\.claude\.claude.json" '{"marker":"keep","mcpServers":{"local-only":{"command":"local-command"}}}'
Write-Fixture "$clientRoot\.codex\config.toml" "# preserve comment`nmodel = 'fixture-model'`n[mcp_servers.local_only]`ncommand = 'local-command'`n"
Write-Fixture "$sourceRoot\mcp.yaml" "servers:`n  shared-docs:`n    url: https://example.invalid/mcp`n    bearerToken:`n      fromEnv: SMOKE_DOCS_TOKEN`n  claude-private:`n    command: private-tool`n    targets: [claude]`n"
$sourceYaml = $sourceRoot.Replace('\', '/')
$clientYaml = $clientRoot.Replace('\', '/')
Write-Fixture "$sourceRoot\config.yaml" @"
git_root: root
sources:
  skills: $sourceYaml/skills
  agents: $sourceYaml/agents
  mcp: ./mcp.yaml
mode: merge
targets:
  claude:
    skills:
      path: $clientYaml/.claude/skills
      exclude: [codex-only]
    agents:
      path: $clientYaml/.claude/agents
      mode: copy
  codex:
    skills:
      path: $clientYaml/.codex/skills
      exclude: [claude-only]
extras:
  - name: instructions
    targets:
      - path: $clientYaml/.claude
      - path: $clientYaml/.codex
mcp:
  targets: [claude, codex]
"@

$preview = Invoke-Fixture @('sync', '--all', '-g', '--dry-run', '--json') 'preview'
$null = $preview.stdout | ConvertFrom-Json
Require (-not (Test-Path "$clientRoot\.claude\AGENTS.md")) 'preview leaves target unchanged'
$synced = Invoke-Fixture @('sync', '--all', '-g', '--json') 'sync'
$null = $synced.stdout | ConvertFrom-Json
foreach ($client in @('claude', 'codex')) {
    Require ([IO.File]::ReadAllText("$clientRoot\.$client\AGENTS.md") -eq 'shared v1') "$client instruction readable"
    Require (Test-Path "$clientRoot\.$client\skills\shared\SKILL.md") "$client shared skill"
}
Require (-not (Test-Path "$clientRoot\.codex\skills\claude-only")) 'Claude skill remains private'
Require (-not (Test-Path "$clientRoot\.claude\skills\codex-only")) 'Codex skill remains private'
Require (Test-Path "$clientRoot\.claude\skills\local-private\SKILL.md") 'local skill preserved'
Require ([IO.File]::ReadAllText("$clientRoot\.claude\agents\private.md") -eq '# Private') 'local agent preserved'
Require ([IO.File]::ReadAllText("$clientRoot\.claude\agents\reviewer.md") -eq '# Reviewer') 'shared agent readable'
$claude = [IO.File]::ReadAllText("$clientRoot\.claude\.claude.json") | ConvertFrom-Json
$codex = [IO.File]::ReadAllText("$clientRoot\.codex\config.toml")
Require ($claude.marker -eq 'keep' -and $null -ne $claude.mcpServers.'local-only') 'Claude native settings preserved'
Require ($codex.Contains('# preserve comment') -and $codex.Contains('fixture-model') -and $codex.Contains('mcp_servers.local_only')) 'Codex native settings preserved'
Require ($null -ne $claude.mcpServers.'claude-private' -and -not $codex.Contains('claude-private')) 'MCP target selection'
Require ($codex.Contains('SMOKE_DOCS_TOKEN') -and $null -ne $claude.mcpServers.'shared-docs') 'MCP environment reference translated'

Write-Fixture "$sourceRoot\extras\instructions\replacement" 'shared v2'
[IO.File]::Move("$sourceRoot\extras\instructions\replacement", "$sourceRoot\extras\instructions\AGENTS.md", $true)
$null = Invoke-Fixture @('sync', 'extras', '-g', '--json') 'refresh'
Require ([IO.File]::ReadAllText("$clientRoot\.codex\AGENTS.md") -eq 'shared v2') 'source replacement refreshes copy'
$instruction = "$clientRoot\.codex\AGENTS.md"
[IO.File]::Delete($instruction)
Write-Fixture $instruction 'local edit'
Write-Fixture "$sourceRoot\extras\instructions\AGENTS.md" 'shared v3'
$null = Invoke-Fixture @('sync', 'extras', '-g', '--json') 'conflict'
Require ([IO.File]::ReadAllText($instruction) -eq 'local edit') 'local instruction edit preserved'
$null = Invoke-Fixture @('sync', 'extras', '-g', '--force', '--json') 'force'
Require ([IO.File]::ReadAllText($instruction) -eq 'shared v3') 'explicit force replaces conflict'

Write-Fixture "$sourceRoot\.gitignore" "config.yaml`n"
foreach ($args in @(@('init'), @('config', 'user.name', 'Fixture'), @('config', 'user.email', 'fixture@example.invalid'), @('add', '-A'), @('commit', '-m', 'fixture'))) {
    & git -C $sourceRoot @args | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "git $args failed" }
}
$remote = "$fixture\remote.git"
& git init --bare $remote | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'remote init failed' }
& git -C $sourceRoot remote add origin $remote
& git -C $sourceRoot push -u origin HEAD | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'fixture push failed' }
[IO.File]::Delete($instruction)
$null = Invoke-Fixture @('pull') 'pull'
Require ([IO.File]::ReadAllText($instruction) -eq 'shared v3') 'root pull repairs extras with up-to-date Git'

[IO.File]::Delete("$clientRoot\.codex\.skillshare-files.json")
[IO.Directory]::CreateDirectory("$clientRoot\.codex\.skillshare-files.json") | Out-Null
$failed = Invoke-Fixture @('sync', 'extras', '-g', '--json') 'failure' $true
$null = $failed.stdout | ConvertFrom-Json
Require ($failed.stdout.Contains('error')) 'failed sync returns JSON error'

$summary = @{ checks = $checks.Count; passed = $checks.ToArray(); fixture = $fixture; binary = $Binary }
Write-Fixture "$fixture\summary.json" ($summary | ConvertTo-Json -Depth 6)
$summary | ConvertTo-Json -Depth 6
