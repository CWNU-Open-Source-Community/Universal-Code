[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'Packaged readiness checks require Windows' }
$tokens = $null
$parseErrors = $null
$harness = Join-Path $PSScriptRoot 'standard-code-packaged-e2e.ps1'
$ast = [System.Management.Automation.Language.Parser]::ParseFile($harness, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw 'Packaged harness parse failed' }

# Load the actual native observer and functions without launching the full package
# harness. These checks use hidden, owned windows and no application/user home.
$native = $ast.Find({ param($node)
    $node -is [System.Management.Automation.Language.CommandAst] -and
        $node.GetCommandName() -eq 'Add-Type' -and
        $node.Extent.Text.Contains('public static class StandardCodePackagedE2ENative')
}, $true)
if ($null -eq $native) { throw 'Native readiness observer not found' }
. ([scriptblock]::Create($native.Extent.Text))
foreach ($name in @('Wait-CandidateReady', 'Get-SafeFailureCode', 'Get-SafeCandidateExitCode',
        'Test-ContainsBytePattern', 'Test-SentinelPersisted')) {
    $definition = $ast.Find({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name
    }, $false)
    if ($null -eq $definition) { throw "Missing harness function: $name" }
    . ([scriptblock]::Create($definition.Extent.Text))
}
$desktopSource = Get-Content -LiteralPath (Join-Path $PSScriptRoot '../cmd/cyberagent-desktop/main_windows.go') -Raw
$windowClass = [regex]::Match($desktopSource, 'WindowClassName:\s*"([^"]+)"').Groups[1].Value
if (-not $windowClass -or -not $native.Extent.Text.Contains('name.ToString() == "' + $windowClass + '"')) {
    throw 'Native readiness class does not match the actual desktop configuration'
}

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Threading;
public static class PackagedReadinessFixture {
    private delegate IntPtr WindowProc(IntPtr window, uint message, IntPtr word, IntPtr longer);
    private static readonly WindowProc Callback = DefWindowProc;
    private static readonly ManualResetEvent BlockedStop = new ManualResetEvent(false);
    private static Thread blockedThread;
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct WindowClass {
        public uint style;
        public WindowProc procedure;
        public int classExtra, windowExtra;
        public IntPtr instance, icon, cursor, background, menu;
        public string name;
    }
    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern ushort RegisterClass(ref WindowClass value);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern IntPtr DefWindowProc(IntPtr window, uint message, IntPtr word, IntPtr longer);
    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr CreateWindowEx(uint extended, string kind, string title, uint style,
        int x, int y, int width, int height, IntPtr parent, IntPtr menu, IntPtr instance, IntPtr parameter);
    [DllImport("user32.dll")]
    public static extern bool DestroyWindow(IntPtr window);
    public static IntPtr CreateHiddenWindow(string kind, bool register) {
        if (register) {
            WindowClass definition = new WindowClass { procedure = Callback, name = kind };
            if (RegisterClass(ref definition) == 0) { throw new Exception("Fixture class registration failed"); }
        }
        // No WS_VISIBLE: the fixture never shows an interactive window.
        IntPtr window = CreateWindowEx(0, kind, "Packaged readiness fixture", 0,
            0, 0, 1, 1, IntPtr.Zero, IntPtr.Zero, IntPtr.Zero, IntPtr.Zero);
        if (window == IntPtr.Zero) { throw new Exception("Hidden fixture window creation failed"); }
        return window;
    }
    public static void StartBlockedWindow(string kind) {
        ManualResetEvent started = new ManualResetEvent(false);
        Exception failure = null;
        blockedThread = new Thread(() => {
            IntPtr window = IntPtr.Zero;
            try {
                window = CreateHiddenWindow(kind, false);
                started.Set();
                BlockedStop.WaitOne(); // Deliberately no message loop on this thread.
            } catch (Exception error) { failure = error; started.Set(); }
            finally { if (window != IntPtr.Zero) { DestroyWindow(window); } }
        });
        blockedThread.IsBackground = true;
        blockedThread.Start();
        if (!started.WaitOne(5000) || failure != null) { throw new Exception("Blocked fixture startup failed", failure); }
    }
    public static void StopBlockedWindow() {
        BlockedStop.Set();
        if (blockedThread != null && !blockedThread.Join(5000)) { throw new Exception("Blocked fixture cleanup failed"); }
    }
}
'@

function Assert-ReadinessRejected {
    param([System.Diagnostics.Process]$Process, [string]$ExpectedCode)
    $caught = $null
    try { $null = Wait-CandidateReady -Process $Process }
    catch { $caught = Get-SafeFailureCode -Message $_.Exception.Message }
    if ($caught -cne $ExpectedCode) { throw "Expected $ExpectedCode, observed $caught" }
}

function Invoke-SentinelRotation {
    param([string]$Root, [string]$RemovedFile, [string]$Sentinel,
        [string]$ReplacementFile = '')
    $rotation = [pscustomobject]@{ done = $false }
    # Rotate after enumeration, before the production scanner opens the file.
    function Get-ChildItem {
        param([string]$LiteralPath, [switch]$File, [switch]$Recurse, [switch]$Force)
        $items = @(Microsoft.PowerShell.Management\Get-ChildItem @PSBoundParameters)
        if (-not $rotation.done) {
            $rotation.done = $true
            [System.IO.File]::Delete($RemovedFile)
            if ($ReplacementFile) {
                [System.IO.File]::WriteAllText($ReplacementFile, $Sentinel)
            }
        }
        return $items
    }
    [System.IO.File]::WriteAllText($RemovedFile, 'temporary cache data')
    try {
        return Test-SentinelPersisted -Roots @($Root) -Sentinels @($Sentinel)
    } finally {
        [System.IO.File]::Delete($RemovedFile)
        if ($ReplacementFile) { [System.IO.File]::Delete($ReplacementFile) }
    }
}

$root = Join-Path ([System.IO.Path]::GetTempPath()) ('packaged-readiness-' + [guid]::NewGuid().ToString('N'))
[System.IO.Directory]::CreateDirectory($root) | Out-Null
$cacheRoot = Join-Path $root 'webview2'
[System.IO.Directory]::CreateDirectory($cacheRoot) | Out-Null
$cacheFile = Join-Path $cacheRoot 'rotating-cache.bin'
$script:database = Join-Path $root 'stale.db'
$sentinelFile = Join-Path $root 'sentinel-evidence.bin'
$StartupTimeoutSeconds = 0.3
$windows = [System.Collections.Generic.List[IntPtr]]::new()
$child = $null
$self = Get-Process -Id $PID
$passed = [System.Collections.Generic.List[string]]::new()
try {
    [System.IO.File]::WriteAllText($script:database, 'pre-existing database is not readiness')
    $child = Start-Process -FilePath (Get-Process -Id $PID).Path -ArgumentList @(
        '-NoProfile', '-NonInteractive', '-Command', 'Start-Sleep -Seconds 30'
    ) -WindowStyle Hidden -PassThru
    # Even an exhausted polling budget must classify the observed state.
    $StartupTimeoutSeconds = 0
    Assert-ReadinessRejected $child 'candidate_window_timeout'
    $StartupTimeoutSeconds = 0.3
    $passed.Add('stale_store_and_headless_process_rejected')

    $windows.Add([PackagedReadinessFixture]::CreateHiddenWindow('#32770', $false))
    Assert-ReadinessRejected $self 'candidate_window_timeout'
    $passed.Add('same_process_error_dialog_rejected')

    $windows.Add([PackagedReadinessFixture]::CreateHiddenWindow($windowClass, $true))
    Assert-ReadinessRejected $child 'candidate_window_timeout'
    $passed.Add('another_process_application_window_rejected')

    Remove-Item -LiteralPath $script:database
    Assert-ReadinessRejected $self 'candidate_startup_timeout'
    $passed.Add('owned_window_without_store_rejected')

    [System.IO.File]::WriteAllText($script:database, 'fixture database')
    $ready = Wait-CandidateReady -Process $self
    if (-not $ready.native_window_ready -or -not $ready.native_window_responsive -or -not $ready.store_nonempty) {
        throw 'Responsive same-process application window was not observed'
    }
    $passed.Add('owned_responsive_window_and_store_observed')

    [void][PackagedReadinessFixture]::DestroyWindow($windows[1])
    $windows.RemoveAt(1)
    [PackagedReadinessFixture]::StartBlockedWindow($windowClass)
    $probeStarted = [DateTime]::UtcNow
    Assert-ReadinessRejected $self 'candidate_window_unresponsive'
    if (([DateTime]::UtcNow - $probeStarted).TotalSeconds -gt 3) { throw 'Blocked message probe was not bounded' }
    [PackagedReadinessFixture]::StopBlockedWindow()
    $passed.Add('blocked_window_thread_rejected_with_bounded_probe')

    $child.Kill($true)
    [void]$child.WaitForExit(5000)
    Assert-ReadinessRejected $child 'candidate_startup_exit'
    $script:activeCandidate = $child
    if ($null -eq (Get-SafeCandidateExitCode)) { throw 'Exited candidate code missing' }
    $passed.Add('exited_candidate_rejected_with_safe_exit_code')

    if ((Get-SafeFailureCode 'private sentinel and local path') -cne 'unexpected_harness_error') {
        throw 'Unknown failure details were exposed'
    }
    $passed.Add('unknown_failure_detail_redacted')

    $sentinel = 'synthetic-packaged-readiness-sentinel'
    foreach ($encoding in @([System.Text.Encoding]::UTF8, [System.Text.Encoding]::Unicode)) {
        [System.IO.File]::WriteAllBytes($sentinelFile, $encoding.GetBytes("prefix-$sentinel-suffix"))
        if (-not (Test-SentinelPersisted -Roots @($root) -Sentinels @($sentinel))) {
            throw 'Persisted sentinel was not detected'
        }
    }
    $passed.Add('utf8_and_utf16_sentinels_detected')
    [System.IO.File]::WriteAllText($sentinelFile, 'no sentinel present')
    if (Test-SentinelPersisted -Roots @($root) -Sentinels @($sentinel)) {
        throw 'Clean evidence reported a persisted sentinel'
    }
    $passed.Add('clean_sentinel_evidence_accepted')

    if (Invoke-SentinelRotation -Root $root -RemovedFile $cacheFile -Sentinel $sentinel) {
        throw 'Removed WebView2 cache file reported a persisted sentinel'
    }
    $passed.Add('removed_webview2_cache_reenumerated')
    if (-not (Invoke-SentinelRotation -Root $root -RemovedFile $cacheFile -Sentinel $sentinel `
            -ReplacementFile (Join-Path $cacheRoot 'replacement-cache.bin'))) {
        throw 'Sentinel in replacement WebView2 cache was missed'
    }
    $passed.Add('replacement_cache_sentinel_detected')
    $rotationFailure = $null
    try {
        $null = Invoke-SentinelRotation -Root $root -RemovedFile (Join-Path $root 'evidence.bin') `
            -Sentinel $sentinel
    } catch { $rotationFailure = $_.Exception }
    if ($null -eq $rotationFailure -or
        (Get-SafeFailureCode -Message $rotationFailure.Message) -cne 'sentinel_evidence_unreadable') {
        throw 'Missing non-cache evidence was not rejected'
    }
    $passed.Add('missing_non_cache_evidence_rejected')

    [System.IO.File]::WriteAllText($cacheFile, 'no sentinel present')
    foreach ($lockedFile in @($sentinelFile, $cacheFile)) {
        foreach ($sharing in @([System.IO.FileShare]::Read, [System.IO.FileShare]::None)) {
            $held = [System.IO.File]::Open($lockedFile, [System.IO.FileMode]::Open,
                [System.IO.FileAccess]::ReadWrite, $sharing)
            try {
                $readFailure = $null
                try { $null = Test-SentinelPersisted -Roots @($root) -Sentinels @($sentinel) }
                catch { $readFailure = $_.Exception }
                if ($null -eq $readFailure -or
                    (Get-SafeFailureCode -Message $readFailure.Message) -cne 'sentinel_evidence_unreadable') {
                    throw 'Unreadable sentinel evidence was not rejected'
                }
                $facts = $readFailure.Data['sentinel_evidence_read']
                if ($null -eq $facts -or $facts.root_index -ne 0 -or
                    $facts.webview2_data -ne ($lockedFile -eq $cacheFile) -or
                    $facts.exception_type -cne 'System.IO.IOException' -or
                    ($facts.hresult -band 0xffff) -ne 32 -or
                    $facts.file_attributes -ne [int](Get-Item -LiteralPath $lockedFile).Attributes) {
                    throw 'Unreadable sentinel evidence did not retain safe failure facts'
                }
                $json = $facts | ConvertTo-Json -Compress
                if ($facts.PSObject.Properties.Name.Count -ne 5 -or
                    $json.Contains($root) -or $json.Contains('sentinel-evidence.bin') -or $json.Contains($sentinel)) {
                    throw 'Sentinel failure facts exposed raw details'
                }
            } finally {
                $held.Dispose()
            }
        }
    }
    $passed.Add('shared_writer_and_exclusive_lock_fail_closed_with_redacted_facts')
    [pscustomobject]@{ passed = $passed.Count; cases = @($passed) } | ConvertTo-Json -Depth 3
} finally {
    [PackagedReadinessFixture]::StopBlockedWindow()
    foreach ($window in $windows) { [void][PackagedReadinessFixture]::DestroyWindow($window) }
    $self.Dispose()
    if ($null -ne $child) {
        $child.Refresh()
        if (-not $child.HasExited) { $child.Kill($true); [void]$child.WaitForExit(5000) }
        $child.Dispose()
    }
    # Only this invocation's known file and empty directory are removed.
    if (Test-Path -LiteralPath $script:database) { Remove-Item -LiteralPath $script:database -Force }
    if (Test-Path -LiteralPath $sentinelFile) { Remove-Item -LiteralPath $sentinelFile -Force }
    if (Test-Path -LiteralPath $cacheFile) { Remove-Item -LiteralPath $cacheFile -Force }
    Remove-Item -LiteralPath $cacheRoot -Force
    Remove-Item -LiteralPath $root -Force
}
