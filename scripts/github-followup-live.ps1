param(
    [ValidateSet('Prepare', 'Run', 'Close')][string]$Action = 'Run',
    [string]$EvidenceRoot = (Join-Path $PSScriptRoot '../output/github-live-acceptance'),
    [string]$Repository = 'CWNU-Open-Source-Community/Universal-Code'
)
$ErrorActionPreference = 'Stop'
$EvidenceRoot = [IO.Path]::GetFullPath($EvidenceRoot)
$manifestPath = Join-Path $EvidenceRoot 'fixture.json'
function Invoke-FixtureGit([string[]]$Arguments) {
    & git -C $script:fixtureRoot @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Fixture git command failed: $($Arguments[0])" }
}
if ($Action -eq 'Prepare') {
    if (Test-Path -LiteralPath $manifestPath) { throw 'Fixture already exists; use its manifest instead of creating another PR.' }
    $stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddHHmmss')
    $base = "Qiyuanqiii/pr-followup-live-base-$stamp"
    $head = "Qiyuanqiii/pr-followup-live-head-$stamp"
    $script:fixtureRoot = Join-Path $EvidenceRoot 'fixture'
    if (Test-Path -LiteralPath $fixtureRoot) { throw 'Fixture directory must be new.' }
    [void](New-Item -ItemType Directory -Path (Join-Path $fixtureRoot '.github/workflows') -Force)
    [IO.File]::WriteAllText((Join-Path $fixtureRoot 'go.mod'), "module followupacceptance`n`ngo 1.23`n")
    [IO.File]::WriteAllText((Join-Path $fixtureRoot 'sum.go'), "package followupacceptance`n`nfunc Sum(a, b int) int { return a + b }`n")
    [IO.File]::WriteAllText((Join-Path $fixtureRoot 'sum_test.go'), @'
package followupacceptance

import "testing"

func TestSum(t *testing.T) {
    for _, c := range []struct{ a, b, want int }{{2, 3, 5}, {-2, 3, 1}, {0, 0, 0}} {
        if got := Sum(c.a, c.b); got != c.want { t.Errorf("Sum(%d,%d)=%d; want %d", c.a, c.b, got, c.want) }
    }
}
'@)
    [IO.File]::WriteAllText((Join-Path $fixtureRoot 'README.md'), "Disposable PR follow-up live acceptance fixture. Isolated base/head branches; never merge into main. The fixed tests and workflow are acceptance oracles.`n")
    [IO.File]::WriteAllText((Join-Path $fixtureRoot '.github/workflows/acceptance.yml'), @'
name: PR follow-up live acceptance
on:
  push:
    branches: ['Qiyuanqiii/pr-followup-live-head-*']
permissions:
  contents: read
jobs:
  test:
    runs-on: ubuntu-latest
    timeout-minutes: 3
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
      - run: go test ./...
'@)
    Invoke-FixtureGit @('init', '-b', $base)
    Invoke-FixtureGit @('remote', 'add', 'origin', "https://github.com/$Repository.git")
    Invoke-FixtureGit @('add', '.')
    Invoke-FixtureGit @('commit', '-m', "test: initialize isolated PR follow-up acceptance`n`nCo-authored-by: Codex <codex@openai.com>")
    Invoke-FixtureGit @('push', 'origin', $base)
    Invoke-FixtureGit @('switch', '-c', $head)
    [IO.File]::WriteAllText((Join-Path $fixtureRoot 'sum.go'), "package followupacceptance`n`nfunc Sum(a, b int) int { return a - b }`n")
    Invoke-FixtureGit @('add', 'sum.go')
    Invoke-FixtureGit @('commit', '-m', "test: introduce isolated Sum regression for live repair`n`nCo-authored-by: Codex <codex@openai.com>")
    Invoke-FixtureGit @('push', '-u', 'origin', $head)
    $bodyPath = Join-Path $EvidenceRoot 'fixture-pr-body.md'
    [IO.File]::WriteAllText($bodyPath, "Disposable acceptance PR for Universal-Code #364 follow-up. A real failing Actions test on an isolated microproject will trigger one authorized model repair and local validation, then a second push and remote read will confirm the new head. Do not merge. No reviewer action requested. This PR will be closed after evidence is collected.`n")
    $prURL = & gh pr create --repo $Repository --base $base --head $head --draft --title '[Acceptance only] PR follow-up real GitHub repair' --body-file $bodyPath
    if ($LASTEXITCODE -ne 0) { throw 'Acceptance PR creation failed; keep the existing branches and inspect before retrying.' }
    $manifest = [ordered]@{protocol='github-followup-live-fixture.v1';repository=$Repository;root=$fixtureRoot;base=$base;head=$head;url=[string]$prURL;number=[int]([string]$prURL).Split('/')[-1];created_at=(Get-Date).ToUniversalTime().ToString('o')}
    $manifest | ConvertTo-Json | Set-Content -LiteralPath $manifestPath -Encoding utf8
    Write-Output $prURL
    return
}
$fixture = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
if ($fixture.protocol -ne 'github-followup-live-fixture.v1' -or $fixture.repository -ne $Repository -or $fixture.head -notlike 'Qiyuanqiii/pr-followup-live-head-*') { throw 'Unexpected acceptance manifest.' }
if ($Action -eq 'Close') {
    & gh pr close ([string]$fixture.number) --repo $Repository
    if ($LASTEXITCODE -ne 0) { throw 'Could not close acceptance PR.' }
    $closedJSON = & gh pr view ([string]$fixture.number) --repo $Repository --json state,closedAt,mergedAt,headRefOid,isDraft,url
    if ($LASTEXITCODE -ne 0) { throw 'Could not verify acceptance PR closure.' }
    $closed = $closedJSON | ConvertFrom-Json
    if ($closed.state -ne 'CLOSED' -or $null -ne $closed.mergedAt) { throw 'Acceptance PR must be closed without merging.' }
    $closedJSON | Set-Content -LiteralPath (Join-Path $EvidenceRoot 'closure.json') -Encoding utf8
    return
}
$env:TRAVERSE_GITHUB_REPAIR_LIVE = '1'
$env:TRAVERSE_GITHUB_LIVE_FIXTURE = $manifestPath
$env:TRAVERSE_GITHUB_LIVE_REPORT = Join-Path $EvidenceRoot 'repair.json'
if (Test-Path -LiteralPath $env:TRAVERSE_GITHUB_LIVE_REPORT) {
    Copy-Item -LiteralPath $env:TRAVERSE_GITHUB_LIVE_REPORT -Destination (Join-Path $EvidenceRoot ('repair-previous-' + (Get-Date).ToUniversalTime().ToString('yyyyMMddHHmmss') + '.json'))
}
Push-Location (Join-Path $PSScriptRoot '..')
try {
    & go test ./internal/application -run '^TestLivePRFollowupRepairsRealGitHubCI$' -count=1 -v -timeout 15m
    if ($LASTEXITCODE -ne 0) { throw 'Live acceptance failed; preserve its workspace and output before retrying.' }
} finally { Pop-Location }
