#requires -Version 7.0
[CmdletBinding()]
param(
    [ValidateSet('Prepare', 'Exercise', 'VerifyRepair')]
    [string]$Mode = 'Prepare',
    [string]$FixtureRoot = '',
    [switch]$IncludeReact,
    [ValidatePattern('^1\.[0-9]+\.[0-9]+$')]
    [string]$GoVersion = '1.26.9',
    [string]$ApiURL = 'http://127.0.0.1:8765/api/v1',
    [string]$RunID = '',
    [ValidateSet('npm', 'go', 'react')]
    [string]$Project = 'npm',
    [string]$ReportPath = '',
    [ValidateRange(15, 900)]
    [int]$TimeoutSeconds = 300
)

<#
.SYNOPSIS
Creates real dependency fixtures or checks an already configured sandbox Run.

.DESCRIPTION
Prepare creates fresh npm and Go repositories with pinned external dependencies,
failing regression tests, and no installed project dependencies. IncludeReact
adds a React/Vite application; only its lockfile metadata is generated on the
host from the repository's pinned lockfile. Import a fixture through the ordinary product UI, select a sandbox, and
start coding before Exercise. Exercise uses the production HTTP API to check
denied downloads, real installation, exact replay, a failing test, and cache
reuse. VerifyRepair runs the unchanged regression test after the actual agent
has repaired the source. API credentials are read from process environment and
excluded from reports. This harness neither configures nor substitutes a model.

.EXAMPLE
./scripts/test-project-preparation.ps1 -Mode Prepare -IncludeReact

.EXAMPLE
./scripts/test-project-preparation.ps1 -Mode Prepare -GoVersion 1.26.0

.EXAMPLE
./scripts/test-project-preparation.ps1 -Mode Exercise -Project npm -RunID run-...
#>

$ErrorActionPreference = 'Stop'
$repositoryRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$utf8 = [Text.UTF8Encoding]::new($false)

function Write-Text([string]$Path, [string]$Content) {
    [IO.Directory]::CreateDirectory((Split-Path -Parent $Path)) | Out-Null
    [IO.File]::WriteAllText($Path, $Content, $utf8)
}

function Write-JSON([string]$Path, [object]$Value) {
    Write-Text $Path (($Value | ConvertTo-Json -Depth 20) + "`n")
}

function Commit-Fixture([string]$Root, [string]$TestID) {
    Write-Text (Join-Path $Root '.gitignore') "node_modules/`ndist/`n.traverse-preparation/`n"
    Write-Text (Join-Path $Root '.prayu/config.yaml') "protocol: project_config.v1`ntest_command_id: $TestID`n"
    & git -C $Root init --initial-branch=main --quiet
    if ($LASTEXITCODE -ne 0) { throw 'Fixture Git initialization failed' }
    & git -C $Root config core.autocrlf false
    if ($LASTEXITCODE -ne 0) { throw 'Fixture Git configuration failed' }
    & git -C $Root add -- .
    if ($LASTEXITCODE -ne 0) { throw 'Fixture Git staging failed' }
    & git -C $Root -c user.name='Preparation acceptance' -c user.email='acceptance@example.invalid' commit --quiet -m 'Add pinned preparation regression fixture'
    if ($LASTEXITCODE -ne 0) { throw 'Fixture Git commit failed' }
}

if ($Mode -eq 'Prepare') {
    if (-not $FixtureRoot) {
        $FixtureRoot = Join-Path $repositoryRoot ('build/project-preparation-acceptance/' + [guid]::NewGuid().ToString('N'))
    }
    $FixtureRoot = [IO.Path]::GetFullPath($FixtureRoot)
    if (Test-Path -LiteralPath $FixtureRoot) { throw 'The fixture root must be a fresh directory' }
    [IO.Directory]::CreateDirectory($FixtureRoot) | Out-Null
    $npmRoot = Join-Path $FixtureRoot 'npm'
    $npmManifest = [ordered]@{ name = 'preparation-number-fixture'; version = '1.0.0'; private = $true
        engines = @{ node = '>=24' }; scripts = @{ test = 'node --test' }; dependencies = @{ 'is-number' = '7.0.0' } }
    Write-JSON (Join-Path $npmRoot 'package.json') $npmManifest
    # Exact published npm metadata for is-number@7.0.0. The broker verifies SRI.
    $npmLock = [ordered]@{ name = $npmManifest.name; version = '1.0.0'; lockfileVersion = 3; requires = $true
        packages = [ordered]@{
            '' = @{ name = $npmManifest.name; version = '1.0.0'; engines = $npmManifest.engines; dependencies = $npmManifest.dependencies }
            'node_modules/is-number' = @{ version = '7.0.0'; resolved = 'https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz'
                integrity = 'sha512-41Cifkg6e8TylSpdtTpeLVMqvSBEVzTttHvERD741+pnZ8ANv0004MRL43QKPDlK9cGvNp6NZWZUBlbGXYxxng=='
                license = 'MIT'; engines = @{ node = '>=0.12.0' } }
        } }
    Write-JSON (Join-Path $npmRoot 'package-lock.json') $npmLock
    Write-Text (Join-Path $npmRoot 'classify.cjs') @'
const isNumber = require('is-number');
exports.classify = (value) => typeof value === 'number' && isNumber(value);
'@
    Write-Text (Join-Path $npmRoot 'classify.test.cjs') @'
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { classify } = require('./classify.cjs');
test('numeric strings are numbers', () => assert.equal(classify('42'), true));
test('ordinary text is not a number', () => assert.equal(classify('hello'), false));
'@
    Write-Text (Join-Path $npmRoot 'README.md') "Fix classify.cjs to accept numeric strings while retaining the nonnumeric regression. Prepare its locked dependency and run the project tests. Do not change the tests.`n"
    Commit-Fixture $npmRoot 'test.npm'

    $goRoot = Join-Path $FixtureRoot 'go'
    Write-Text (Join-Path $goRoot 'go.mod') "module example.com/preparation-uuid`n`ngo $GoVersion`n`nrequire github.com/google/uuid v1.6.0`n"
    Write-Text (Join-Path $goRoot 'go.sum') @'
github.com/google/uuid v1.6.0 h1:NIvaJDMOsjHA8n1jAhLSgzrAzy1Hgr+hNrb57e+94F0=
github.com/google/uuid v1.6.0/go.mod h1:TIyPZe4MgqvfeYDBFedMoGGpEw/LqOeaOT+nhxU+yHo=
'@
    Write-Text (Join-Path $goRoot 'normalize.go') @'
package uuidfixture
import "github.com/google/uuid"
func Normalize(value string) (string, error) {
    _, err := uuid.Parse(value)
    return value, err
}
'@
    Write-Text (Join-Path $goRoot 'normalize_test.go') @'
package uuidfixture
import "testing"
func TestNormalizeCanonicalUUID(t *testing.T) {
    actual, err := Normalize("6BA7B810-9DAD-11D1-80B4-00C04FD430C8")
    if err != nil { t.Fatal(err) }
    if actual != "6ba7b810-9dad-11d1-80b4-00c04fd430c8" { t.Fatalf("canonical UUID: %s", actual) }
}
'@
    Write-Text (Join-Path $goRoot 'README.md') "Return the parsed UUID in canonical lowercase form from Normalize. Prepare the locked module and run the unchanged regression test.`n"
    Commit-Fixture $goRoot 'test.go'

    $reactRoot = ''
    if ($IncludeReact) {
        $reactRoot = Join-Path $FixtureRoot 'react'
        $reactManifest = [ordered]@{
            name = 'preparation-react-fixture'; version = '1.0.0'; private = $true; type = 'module'; engines = @{ node = '>=24' }
            scripts = @{ test = 'node --test && vite build'; dev = 'vite --host 127.0.0.1' }
            dependencies = @{ react = '19.2.8'; 'react-dom' = '19.2.8' }; devDependencies = @{ vite = '8.2.1' }
        }
        Write-JSON (Join-Path $reactRoot 'package.json') $reactManifest
        Write-Text (Join-Path $reactRoot 'index.html') '<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>计数器</title><div id="root"></div><script type="module" src="/src/main.jsx"></script></html>'
        Write-Text (Join-Path $reactRoot 'src/main.jsx') @'
import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { add } from './counter.js';
function App() {
  const [count, setCount] = useState(0);
  return <main><h1>计数器</h1><output>{count}</output><button onClick={() => setCount(add(count, 1))}>增加 1</button></main>;
}
createRoot(document.getElementById('root')).render(<App />);
'@
        Write-Text (Join-Path $reactRoot 'src/counter.js') 'export const add = (value, step) => value - step;'
        Write-Text (Join-Path $reactRoot 'counter.test.js') @'
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { add } from './src/counter.js';
test('counter increments by the requested step', () => assert.equal(add(2, 3), 5));
'@
        Write-Text (Join-Path $reactRoot 'README.md') "Fix add in src/counter.js so the button increases the counter. Prepare dependencies, keep the regression test, and run npm test including the Vite production build.`n"
        # Retain the exact published dependency closure already pinned by this
        # repository. Fixture creation downloads or executes no package content.
        $reference = Get-Content -LiteralPath (Join-Path $repositoryRoot 'web/package-lock.json') -Raw | ConvertFrom-Json -AsHashtable
        $selected = [ordered]@{ '' = @{ name = $reactManifest.name; version = $reactManifest.version; engines = $reactManifest.engines
            dependencies = $reactManifest.dependencies; devDependencies = $reactManifest.devDependencies } }
        $queue = [Collections.Generic.Queue[string]]::new()
        foreach ($name in @('react', 'react-dom', 'vite')) {
            $entry = $reference.packages['node_modules/' + $name]
            $pin = if ($name -eq 'vite') { $reactManifest.devDependencies[$name] } else { $reactManifest.dependencies[$name] }
            if ($null -eq $entry -or $entry.version -ne $pin) { throw 'React fixture versions must match the repository lockfile' }
            $queue.Enqueue('node_modules/' + $name)
        }
        while ($queue.Count -gt 0) {
            $packagePath = $queue.Dequeue()
            if ($selected.Contains($packagePath)) { continue }
            $entry = $reference.packages[$packagePath]
            if ($null -eq $entry -or $entry.link -or -not $entry.resolved -or -not $entry.integrity) { throw 'Fixture dependency is not pinned to a public artifact' }
            $selected[$packagePath] = $entry
            foreach ($field in @('dependencies', 'optionalDependencies', 'peerDependencies')) {
                if ($null -eq $entry[$field]) { continue }
                foreach ($name in @($entry[$field].Keys)) {
                    if ($field -eq 'peerDependencies' -and $null -ne $entry.peerDependenciesMeta -and $entry.peerDependenciesMeta[$name].optional) { continue }
                    $ancestor = $packagePath
                    do {
                        $candidate = $(if ($ancestor) { $ancestor + '/' } else { '' }) + 'node_modules/' + $name
                        if ($reference.packages.ContainsKey($candidate)) { $queue.Enqueue($candidate); break }
                        $ancestor = $ancestor -replace '(^|/)node_modules/(@[^/]+/)?[^/]+$', ''
                    } while ($ancestor)
                    if (-not $ancestor -and -not $reference.packages.ContainsKey($candidate)) {
                        $candidate = 'node_modules/' + $name
                        if ($reference.packages.ContainsKey($candidate)) { $queue.Enqueue($candidate) }
                        elseif ($field -eq 'dependencies') { throw "The reference lockfile lacks $name required by $packagePath" }
                    }
                }
            }
            if ($selected.Count -gt 4096) { throw 'React fixture exceeds the package limit' }
        }
        Write-JSON (Join-Path $reactRoot 'package-lock.json') @{ name = $reactManifest.name; version = '1.0.0'; lockfileVersion = 3; requires = $true; packages = $selected }
        if (Test-Path -LiteralPath (Join-Path $reactRoot 'node_modules')) { throw 'Fixture creation unexpectedly installed React dependencies' }
        Commit-Fixture $reactRoot 'test.npm'
    }
    $files = Get-ChildItem -LiteralPath $FixtureRoot -Recurse -File | Where-Object { $_.FullName -notmatch '[\\/]\.git[\\/]' } |
        ForEach-Object { @{ path = [IO.Path]::GetRelativePath($FixtureRoot, $_.FullName); sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant() } }
    $summary = [ordered]@{ fixture_version = 1; prepared_at = [DateTime]::UtcNow.ToString('o'); npm = $npmRoot; go = $goRoot
        react = $reactRoot; project_dependencies_installed = $false; files = @($files) }
    Write-JSON (Join-Path $FixtureRoot 'fixtures.json') $summary
    Write-Output ($summary | ConvertTo-Json -Depth 4)
    return
}

if (-not $RunID) { throw '-RunID is required for API acceptance' }
$api = [Uri]$ApiURL
if ($api.Scheme -notin @('http', 'https') -or $api.Host -notin @('127.0.0.1', 'localhost', '::1') -or $api.UserInfo -or $api.Query -or $api.Fragment) {
    throw '-ApiURL must identify the authenticated loopback API'
}
$readToken = [Environment]::GetEnvironmentVariable('CYBERAGENT_API_READ_TOKEN', 'Process')
if (-not $readToken) { $readToken = [Environment]::GetEnvironmentVariable('CYBERAGENT_API_TOKEN', 'Process') }
$controlToken = [Environment]::GetEnvironmentVariable('CYBERAGENT_API_CONTROL_TOKEN', 'Process')
if (-not $readToken -or -not $controlToken -or $readToken -eq $controlToken) { throw 'Separate API read and control tokens are required in the process environment' }
if (-not $ReportPath) { $ReportPath = Join-Path $repositoryRoot ('build/project-preparation-acceptance/result-' + [guid]::NewGuid().ToString('N') + '.json') }
$checks = [Collections.Generic.List[object]]::new()
$completed = $false
$runPath = '/runs/' + [Uri]::EscapeDataString($RunID)

function Request-API([string]$Path, [string]$Method = 'GET', [object]$Body = $null, [string]$Key = '') {
    $headers = @{ Authorization = 'Bearer ' + $(if ($Method -eq 'GET') { $readToken } else { $controlToken }) }
    if ($Key) { $headers['Idempotency-Key'] = $Key }
    $arguments = @{ Uri = $ApiURL.TrimEnd('/') + $Path; Method = $Method; Headers = $headers; TimeoutSec = 30 }
    if ($null -ne $Body) { $arguments.Body = $Body | ConvertTo-Json -Depth 10 -Compress; $arguments.ContentType = 'application/json' }
    $response = Invoke-RestMethod @arguments
    if ($response.version -ne 'api.v1' -or $null -eq $response.data) { throw 'API response envelope is invalid' }
    return $response.data
}

function Wait-Preparation([string]$ID) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        $value = Request-API "$runPath/project-preparation/operations/$([Uri]::EscapeDataString($ID))"
        if ($value.id -ne $ID -or $value.run_id -ne $RunID) { throw 'Preparation observation identity mismatch' }
        if ($value.state -notin @('pending', 'downloading', 'installing')) { return $value }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'Preparation did not finish within its acceptance timeout'
}

function Test-ProjectAction([bool]$ExpectPass) {
    $key = 'preparation-acceptance-test-' + [guid]::NewGuid().ToString('N')
    $started = Request-API "$runPath/project-actions/test" 'POST' @{ version = 'project-actions.v1'; target = ''; confirm_execution = $true } $key
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        $result = Request-API "$runPath/project-actions/jobs/$([Uri]::EscapeDataString($started.job.id))"
        if ($result.job.state -notin @('prepared', 'preparing', 'running', 'stopping')) { break }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($result.job.state -in @('prepared', 'preparing', 'running', 'stopping')) { throw 'Project test did not finish within its acceptance timeout' }
    $passed = $result.job.state -eq 'completed' -and $result.job.exit_code -eq 0
    $expectedMarker = switch ($Project) { 'go' { 'canonical UUID' } 'react' { 'counter increments by the requested step' } default { 'numeric strings are numbers' } }
    $expectedFailure = -not $passed -and $result.job.exit_code -eq 1 -and
        ([string]$result.job.stdout + [string]$result.job.stderr).Contains($expectedMarker)
    $matches = if ($ExpectPass) { $passed } else { $expectedFailure }
    $checks.Add(@{ name = $(if ($ExpectPass) { 'repaired-project-test' } else { 'original-regression-fails' }); passed = $matches
        job_id = $result.job.id; state = $result.job.state; exit_code = $result.job.exit_code })
    if (-not $matches) { throw 'Project test result did not match the acceptance expectation' }
}

try {
    if ($Mode -eq 'VerifyRepair') { Test-ProjectAction $true }
    else {
        $view = Request-API "$runPath/project-preparation?target=."
        if (-not $view.available -or -not $view.plan.ready) { throw ('Project preparation is unavailable: ' + $view.reason) }
        if ($view.cache.cached_packages -ne 0 -or $view.plan.package_count -lt 1) { throw 'Missing-cache acceptance requires an isolated application home and external dependencies' }
        $body = @{ version = 'project-preparation.v1'; target = '.'; expected_fingerprint = $view.plan.fingerprint
            allowed_sources = @(); confirm_execution = $true; run_install_scripts = $false }
        $denied = Request-API "$runPath/project-preparation" 'POST' $body ('preparation-acceptance-denied-' + [guid]::NewGuid().ToString('N'))
        $deniedResult = Wait-Preparation $denied.latest.id
        $sourceDenied = $deniedResult.state -eq 'failed' -and $deniedResult.downloaded_bytes -eq 0 -and
            ([string]$deniedResult.error).Contains('package source has not been approved')
        $checks.Add(@{ name = 'unapproved-download-denied'; passed = $sourceDenied; operation_id = $deniedResult.id
            state = $deniedResult.state; downloaded_bytes = $deniedResult.downloaded_bytes; error = $deniedResult.error })
        if (-not $sourceDenied) { throw 'An unapproved missing package must be denied by source authorization without downloading' }
        $body.allowed_sources = @($view.plan.sources)
        $body.run_install_scripts = $Project -eq 'react'
        $installKey = 'preparation-acceptance-install-' + [guid]::NewGuid().ToString('N')
        $started = Request-API "$runPath/project-preparation" 'POST' $body $installKey
        $installed = Wait-Preparation $started.latest.id
        $checks.Add(@{ name = 'real-sandbox-install'; passed = $installed.state -eq 'ready'; operation_id = $installed.id
            state = $installed.state; downloaded_bytes = $installed.downloaded_bytes; package_count = $installed.total_packages; job_id = $installed.job.id })
        if ($installed.state -ne 'ready') { throw ('Real sandbox installation failed: ' + $installed.error) }
        $replayed = Request-API "$runPath/project-preparation" 'POST' $body $installKey
        $checks.Add(@{ name = 'same-key-replay'; passed = $replayed.latest.id -eq $installed.id; operation_id = $replayed.latest.id })
        if ($replayed.latest.id -ne $installed.id) { throw 'Replay created a different preparation operation' }
        Test-ProjectAction $false
        $body.allowed_sources = @()
        $reused = Request-API "$runPath/project-preparation" 'POST' $body ('preparation-acceptance-cache-' + [guid]::NewGuid().ToString('N'))
        $cached = Wait-Preparation $reused.latest.id
        $cachePass = $cached.state -eq 'ready' -and $cached.cache_hits -eq $cached.total_packages -and $cached.downloaded_bytes -eq 0
        $checks.Add(@{ name = 'offline-cache-reuse'; passed = $cachePass; operation_id = $cached.id; state = $cached.state
            cache_hits = $cached.cache_hits; total_packages = $cached.total_packages; downloaded_bytes = $cached.downloaded_bytes })
        if (-not $cachePass) { throw ('Cached preparation failed: ' + $cached.error) }
    }
    $completed = $true
} finally {
    Write-JSON $ReportPath ([ordered]@{ acceptance_version = 1; mode = $Mode; project = $Project; run_id = $RunID
        recorded_at = [DateTime]::UtcNow.ToString('o'); checks = $checks.ToArray(); completed = $completed
        passed = $completed -and $checks.Count -gt 0 -and @($checks | Where-Object { -not $_.passed }).Count -eq 0 })
    Write-Output "acceptance_report: $ReportPath"
}
