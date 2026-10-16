param(
    [string]$Step
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$planPath = [IO.Path]::Combine($PSScriptRoot, "test-local.plan.json")
$runtimeRoot = [IO.Path]::Combine($repoRoot, "Tmp", "test-runtime")
$runId = "{0}-{1}" -f $PID, [Guid]::NewGuid().ToString("N")
$runRoot = [IO.Path]::Combine($runtimeRoot, "runs", $runId)
$cacheRoot = [IO.Path]::Combine($runtimeRoot, "cache")
$recordPath = [IO.Path]::Combine($runRoot, "run.json")

function Assert-Keys([object]$Value, [string[]]$Allowed, [string[]]$Required, [string]$Context) {
    if ($null -eq $Value -or $Value -is [array]) { throw "$Context must be a JSON object." }
    $names = @($Value.PSObject.Properties | ForEach-Object { $_.Name })
    foreach ($name in $names) { if ($Allowed -cnotcontains $name) { throw "$Context has unsupported property '$name'." } }
    foreach ($name in $Required) { if ($names -cnotcontains $name) { throw "$Context is missing '$name'." } }
}

function Get-HostPlatform {
    if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Linux)) { $os = "linux" }
    elseif ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Windows)) { $os = "windows" }
    elseif ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::OSX)) { $os = "darwin" }
    else { $os = "unsupported" }
    $architecture = ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture).ToString().ToLowerInvariant()
    $goArchitecture = switch ($architecture) {
        "x64" { "amd64"; break }
        "x86" { "386"; break }
        default { $architecture }
    }
    return [pscustomobject]@{ Os = $os; Architecture = $architecture; GoArchitecture = $goArchitecture }
}

function Get-TestFiles([string]$Root) {
    $paths = @(& git -C $Root -c core.quotepath=false ls-files --cached --others --exclude-standard)
    if ($LASTEXITCODE -ne 0) { throw "git ls-files failed while checking test coverage." }
    $ignoredPaths = @(& git -C $Root -c core.quotepath=false ls-files --others --ignored --exclude-standard)
    if ($LASTEXITCODE -ne 0) { throw "git ls-files failed while checking ignored test files." }
    $files = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($value in @($paths) + @($ignoredPaths)) {
        $path = ([string]$value).Replace("\", "/")
        if ($path.StartsWith("Tmp/test-runtime/", [StringComparison]::OrdinalIgnoreCase)) { continue }
        if ($path -cmatch "_test\.go$") {
            if ([IO.Path]::IsPathRooted($path) -or $path -match "(^|/)\.\.(/|$)") { throw "Unsafe test path from git: $path" }
            if ([IO.File]::Exists([IO.Path]::Combine($Root, $path))) { [void]$files.Add($path) }
        }
    }
    return @($files | Sort-Object -CaseSensitive)
}

function Test-FilePattern([string]$Path, [string]$Pattern, [string]$Platform) {
    $options = if ($Platform -eq "windows") { [Management.Automation.WildcardOptions]::IgnoreCase } else { [Management.Automation.WildcardOptions]::None }
    return ([Management.Automation.WildcardPattern]::new($Pattern, $options)).IsMatch($Path)
}

function Invoke-Go(
    [string[]]$Arguments,
    [int]$TimeoutSeconds,
    [System.Collections.IDictionary]$Environment
) {
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = "go"
    $startInfo.WorkingDirectory = $repoRoot
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in $Arguments) { $startInfo.ArgumentList.Add([string]$argument) }
    foreach ($name in $Environment.Keys) { $startInfo.Environment[[string]$name] = [string]$Environment[$name] }

    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    $timer = [Diagnostics.Stopwatch]::StartNew()
    try {
        if (-not $process.Start()) { throw "Could not start go $($Arguments -join ' ')." }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $completed = $process.WaitForExit($TimeoutSeconds * 1000)
        $timedOut = -not $completed
        if (-not $completed) {
            try { $process.Kill($true) } catch { }
            $stopped = $false
            try { $stopped = $process.WaitForExit(5000) } catch { }
            if (-not $stopped) {
                try { if (-not $process.HasExited) { $process.Kill() } } catch { }
                try { $stopped = $process.WaitForExit(2000) } catch { }
            }
            if (-not $stopped) {
                try { $process.StandardOutput.Close() } catch { }
                try { $process.StandardError.Close() } catch { }
                $timer.Stop()
                return [pscustomobject]@{ TimedOut = $true; ExitCode = $null; DurationMs = $timer.ElapsedMilliseconds; Stdout = "" }
            }
        }
        $streamsTask = [System.Threading.Tasks.Task]::WhenAll([System.Threading.Tasks.Task[]]@($stdoutTask, $stderrTask))
        $drained = $false
        try { $drained = $streamsTask.Wait(5000) } catch { }
        if (-not $drained) {
            try { $process.StandardOutput.Close() } catch { }
            try { $process.StandardError.Close() } catch { }
            if ($timedOut) {
                $timer.Stop()
                return [pscustomobject]@{ TimedOut = $true; ExitCode = $process.ExitCode; DurationMs = $timer.ElapsedMilliseconds; Stdout = "" }
            }
            throw "Go exited, but its output streams did not close within 5 seconds."
        }
        $timer.Stop()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        [Console]::Out.Write($stdout)
        [Console]::Error.Write($stderr)
        return [pscustomobject]@{ TimedOut = $timedOut; ExitCode = $process.ExitCode; DurationMs = $timer.ElapsedMilliseconds; Stdout = $stdout }
    } finally {
        $timer.Stop()
        $process.Dispose()
    }
}

function Assert-StringArray([object]$Value, [string]$Context) {
    if ($Value -isnot [array]) { throw "$Context must be an array." }
    foreach ($entry in $Value) { if ($entry -isnot [string]) { throw "$Context entries must be strings." } }
}

$hostPlatform = Get-HostPlatform
$startedAt = [DateTimeOffset]::UtcNow.ToString("o")
$runRecord = [ordered]@{
    runId = $runId
    planSha256 = $null
    sourceHead = $null
    mode = $null
    requestedStep = $null
    platform = [ordered]@{ os = $hostPlatform.Os; architecture = $hostPlatform.Architecture; goVersion = $null }
    deferredSteps = @()
    steps = [System.Collections.Generic.List[object]]::new()
    result = "running"
    error = $null
    startedAtUtc = $startedAt
    finishedAtUtc = $null
}
$runRecord.mode = if ($PSBoundParameters.ContainsKey("Step")) { "step" } else { "full" }
$runRecord.requestedStep = if ($PSBoundParameters.ContainsKey("Step")) { $Step } else { $null }
$failure = $null
$recordReady = $false

try {
    foreach ($path in @($runRoot, [IO.Path]::Combine($runRoot, "tmp"), [IO.Path]::Combine($runRoot, "go-tmp"), [IO.Path]::Combine($cacheRoot, "go-build"), [IO.Path]::Combine($cacheRoot, "go-mod"))) {
        [void][IO.Directory]::CreateDirectory($path)
    }
    $recordReady = $true
    if ($hostPlatform.Os -notin @("linux", "windows", "darwin")) { throw "Unsupported operating system." }
    if ($hostPlatform.GoArchitecture -notin @("386", "amd64", "arm", "arm64", "loong64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x", "wasm")) { throw "Unsupported native Go architecture: $($hostPlatform.GoArchitecture)" }

    $bytes = [IO.File]::ReadAllBytes($planPath)
    $hasher = [Security.Cryptography.SHA256]::Create()
    try { $hash = [BitConverter]::ToString($hasher.ComputeHash($bytes)).Replace("-", "").ToLowerInvariant() } finally { $hasher.Dispose() }
    $runRecord.planSha256 = $hash
    Write-Host "[test-local] plan sha256: $hash"
    try {
        $head = (& git -C $repoRoot rev-parse --verify HEAD 2>$null | Select-Object -First 1)
        if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace([string]$head)) { $runRecord.sourceHead = ([string]$head).Trim() }
    } catch { $runRecord.sourceHead = $null }

    $json = [Text.Encoding]::UTF8.GetString($bytes).TrimStart([char]0xFEFF)
    $plan = ConvertFrom-Json -InputObject $json -ErrorAction Stop
    Assert-Keys $plan @("version", "steps", "impact") @("version", "steps") "Canonical plan"
    if (($plan.version -isnot [int] -and $plan.version -isnot [long]) -or $plan.version -ne 2) { throw "Unsupported canonical plan version: $($plan.version)" }
    if ($plan.steps -isnot [array] -or $plan.steps.Count -eq 0) { throw "Canonical plan steps must be a nonempty array." }
    if ($plan.PSObject.Properties.Name -contains "impact") {
        Assert-Keys $plan.impact @("globalPaths", "ignore") @("globalPaths", "ignore") "Plan impact"
        Assert-StringArray $plan.impact.globalPaths "Plan impact.globalPaths"
        if ($plan.impact.ignore -isnot [array]) { throw "Plan impact.ignore must be an array." }
        foreach ($entry in $plan.impact.ignore) {
            Assert-Keys $entry @("path", "reason") @("path", "reason") "Plan impact.ignore entry"
            if ($entry.path -isnot [string] -or [string]::IsNullOrWhiteSpace($entry.path) -or $entry.path -match "(^|/)\.\.(/|$)") { throw "Plan impact.ignore path is invalid." }
            if ($entry.reason -isnot [string] -or [string]::IsNullOrWhiteSpace($entry.reason)) { throw "Plan impact.ignore reason is required." }
        }
    }

    $steps = [System.Collections.Generic.List[object]]::new()
    $names = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $patterns = [System.Collections.Generic.List[string]]::new()
    $patternNames = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $protected = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($variable in @("GOOS", "GOARCH", "GOFLAGS", "GOWORK", "GOENV", "TEMP", "TMP", "TMPDIR", "GOTMPDIR", "GOCACHE", "GOMODCACHE")) { [void]$protected.Add($variable) }

    foreach ($item in $plan.steps) {
        Assert-Keys $item @("name", "workingDirectory", "filePath", "arguments", "environment", "testFiles", "tier", "impact", "resourceLocks", "timeoutSeconds", "alwaysRun", "safetyGate", "platform") @("name", "workingDirectory", "filePath", "arguments", "tier", "impact", "resourceLocks", "timeoutSeconds") "Plan step"
        $name = [string]$item.name
        if ([string]::IsNullOrWhiteSpace($name) -or -not $names.Add($name)) { throw "Plan step names must be nonempty and unique: '$name'." }
        if ($item.workingDirectory -cne "." -or $item.filePath -cne "go") { throw "Plan step '$name' must run go from the repository root." }
        if ($item.arguments -isnot [array] -or $item.arguments.Count -ne 2 -or $item.arguments[0] -cnotin @("test", "vet", "build") -or $item.arguments[1] -cne "./...") { throw "Plan step '$name' must be go test, go vet, or go build ./... ." }
        if ($item.tier -isnot [string] -or $item.tier -cnotin @("fast", "related", "heavy")) { throw "Plan step '$name' has an invalid tier." }
        Assert-Keys $item.impact @("paths", "packages", "globalPaths") @("paths", "packages", "globalPaths") "Plan step '$name' impact"
        foreach ($key in @("paths", "packages", "globalPaths")) { Assert-StringArray $item.impact.$key "Plan step '$name' impact.$key" }
        if ($item.resourceLocks -isnot [array]) { throw "Plan step '$name' resourceLocks must be an array." }
        foreach ($lock in $item.resourceLocks) { if ($lock -isnot [string] -or [string]::IsNullOrWhiteSpace($lock)) { throw "Plan step '$name' has an invalid resource lock." } }
        if (($item.timeoutSeconds -isnot [int] -and $item.timeoutSeconds -isnot [long]) -or $item.timeoutSeconds -le 0 -or $item.timeoutSeconds -gt 2147483) { throw "Plan step '$name' timeoutSeconds must be from 1 through 2147483." }
        foreach ($flag in @("alwaysRun", "safetyGate")) { if ($item.PSObject.Properties.Name -contains $flag -and $item.$flag -isnot [bool]) { throw "Plan step '$name' $flag must be boolean." } }

        if ($item.PSObject.Properties.Name -contains "platform") {
            if ($item.platform -isnot [array] -or $item.platform.Count -eq 0) { throw "Plan step '$name' platform must be a nonempty array." }
            $platformSet = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
            foreach ($value in $item.platform) {
                if ($value -isnot [string] -or @("linux", "windows", "darwin") -cnotcontains $value -or -not $platformSet.Add($value)) { throw "Plan step '$name' has an invalid or duplicate platform." }
            }
            $platforms = @($platformSet)
        } else { $platforms = @("linux", "windows", "darwin") }
        $command = [string]$item.arguments[0]
        if ($command -eq "test" -and $platforms -ccontains "windows") { throw "Go test steps cannot target Windows." }

        $environment = [ordered]@{}
        if ($item.PSObject.Properties.Name -contains "environment") {
            if ($null -eq $item.environment -or $item.environment -is [array]) { throw "Plan step '$name' environment must be an object." }
            foreach ($variable in $item.environment.PSObject.Properties) {
                if ($variable.Name -notmatch "^[A-Za-z_][A-Za-z0-9_]*$" -or $variable.Value -isnot [string] -or $protected.Contains($variable.Name)) { throw "Plan step '$name' has an invalid or protected environment entry." }
                $environment[$variable.Name] = $variable.Value
            }
        }

        if ($item.PSObject.Properties.Name -contains "testFiles") {
            if ($command -ne "test" -or $item.testFiles -isnot [array] -or $item.testFiles.Count -eq 0) { throw "Only go test steps may register testFiles." }
            foreach ($patternValue in $item.testFiles) {
                if ($patternValue -isnot [string]) { throw "Plan step '$name' testFiles entries must be strings." }
                $pattern = $patternValue.Replace("\", "/")
                if ([string]::IsNullOrWhiteSpace($pattern) -or [IO.Path]::IsPathRooted($pattern) -or $pattern -match "^[A-Za-z]:" -or $pattern -match "(^|/)\.\.(/|$)") { throw "Plan step '$name' has an unsafe testFiles pattern." }
                if (-not $patternNames.Add($pattern)) { throw "Duplicate testFiles pattern: $pattern" }
                $patterns.Add($pattern)
            }
        } elseif ($command -eq "test") { throw "Go test step '$name' must register testFiles." }
        $steps.Add([pscustomobject]@{ Name = $name; Command = $command; Args = @([string]$item.arguments[0], [string]$item.arguments[1]); Environment = $environment; Timeout = [int]$item.timeoutSeconds; Platforms = $platforms })
    }

    $testFiles = @(Get-TestFiles $repoRoot)
    foreach ($pattern in $patterns) {
        if (@($testFiles | Where-Object { Test-FilePattern $_ $pattern $hostPlatform.Os }).Count -eq 0) { throw "Unknown testFiles pattern: $pattern" }
    }
    foreach ($testFile in $testFiles) {
        $matches = @($patterns | Where-Object { Test-FilePattern $testFile $_ $hostPlatform.Os })
        if ($matches.Count -eq 0) { throw "Uncovered test file: $testFile" }
        if ($matches.Count -gt 1) { throw "Test file is covered more than once: $testFile" }
    }

    $runRecord.deferredSteps = @($steps | Where-Object { $_.Platforms -cnotcontains $hostPlatform.Os -or ($PSBoundParameters.ContainsKey("Step") -and $_.Name -cne $Step) } | ForEach-Object {
        $reason = if ($_.Platforms -cnotcontains $hostPlatform.Os) { "Current platform '$($hostPlatform.Os)' is not declared." } else { "Not selected by -Step '$Step'." }
        [pscustomobject]@{ name = $_.Name; platforms = $_.Platforms; reason = $reason }
    })
    if ($PSBoundParameters.ContainsKey("Step")) {
        if ([string]::IsNullOrWhiteSpace($Step)) { throw "Step must name one canonical test step." }
        $selected = @($steps | Where-Object { $_.Name -ceq $Step })
        if ($selected.Count -eq 0) { throw "Unknown canonical test step: $Step" }
        if ($selected[0].Platforms -cnotcontains $hostPlatform.Os) { throw "Step '$Step' is not supported on '$($hostPlatform.Os)'." }
    } else { $selected = @($steps | Where-Object { $_.Platforms -ccontains $hostPlatform.Os }) }
    if ($selected.Count -eq 0) { throw "Canonical plan has no steps for '$($hostPlatform.Os)'." }

    foreach ($item in $selected) {
        $runRecord.steps.Add([ordered]@{ name = $item.Name; command = @("go") + $item.Args; timeoutSeconds = $item.Timeout; status = "pending"; exitCode = $null; durationMs = $null; error = $null })
    }
    $baseEnvironment = [ordered]@{
        GOOS = $hostPlatform.Os; GOARCH = $hostPlatform.GoArchitecture; GOFLAGS = ""; GOWORK = "off"; GOENV = "off"
        TEMP = [IO.Path]::Combine($runRoot, "tmp"); TMP = [IO.Path]::Combine($runRoot, "tmp"); TMPDIR = [IO.Path]::Combine($runRoot, "tmp")
        GOTMPDIR = [IO.Path]::Combine($runRoot, "go-tmp"); GOCACHE = [IO.Path]::Combine($cacheRoot, "go-build"); GOMODCACHE = [IO.Path]::Combine($cacheRoot, "go-mod")
    }

    $versionResult = Invoke-Go @("version") 30 $baseEnvironment
    if ($versionResult.TimedOut -or $versionResult.ExitCode -ne 0) { throw "Go version probe failed or timed out." }
    $versionMatch = [regex]::Match($versionResult.Stdout, "(?m)^go version (?<version>\S+) (?<os>\S+)/(?<arch>\S+)\s*$")
    if (-not $versionMatch.Success -or $versionMatch.Groups["os"].Value -cne $hostPlatform.Os -or $versionMatch.Groups["arch"].Value -cne $hostPlatform.GoArchitecture) { throw "Go tool host does not match the native runner." }
    $runRecord.platform.goVersion = $versionMatch.Groups["version"].Value

    for ($index = 0; $index -lt $selected.Count; $index++) {
        $item = $selected[$index]
        $stepRecord = $runRecord.steps[$index]
        $stepRecord.status = "running"
        $clock = [Diagnostics.Stopwatch]::StartNew()
        Write-Host "[test-local] step: $($item.Name) ($($hostPlatform.Os))"
        try {
            $environment = [ordered]@{}
            foreach ($key in $item.Environment.Keys) { $environment[$key] = $item.Environment[$key] }
            foreach ($key in $baseEnvironment.Keys) { $environment[$key] = $baseEnvironment[$key] }
            $result = Invoke-Go $item.Args $item.Timeout $environment
            $stepRecord.exitCode = $result.ExitCode
            $stepRecord.durationMs = $result.DurationMs
            if ($result.TimedOut) { $stepRecord.status = "timeout"; $stepRecord.error = "Timed out; process tree termination attempted."; throw "Step '$($item.Name)' timed out." }
            if ($result.ExitCode -ne 0) { $stepRecord.status = "failed"; $stepRecord.error = "Exit code $($result.ExitCode)."; throw "Step '$($item.Name)' failed with exit code $($result.ExitCode)." }
            $stepRecord.status = "passed"
        } catch {
            if ($stepRecord.status -eq "running") { $stepRecord.status = "failed"; $stepRecord.error = $_.Exception.Message }
            throw
        } finally {
            $clock.Stop()
            if ($null -eq $stepRecord.durationMs) { $stepRecord.durationMs = $clock.ElapsedMilliseconds }
        }
    }
    $runRecord.result = "passed"
} catch {
    $failure = $_
    $runRecord.result = "failed"
    $runRecord.error = $_.Exception.Message
    foreach ($entry in $runRecord.steps) {
        if ($entry.status -eq "pending") { $entry.status = "blocked"; $entry.error = "A previous check failed." }
    }
} finally {
    $runRecord.finishedAtUtc = [DateTimeOffset]::UtcNow.ToString("o")
    if ($recordReady) {
        try {
            $recordJson = ConvertTo-Json -InputObject $runRecord -Depth 8
            [IO.File]::WriteAllText($recordPath, $recordJson, [Text.UTF8Encoding]::new($false))
            Write-Host "[test-local] plan sha256: $($runRecord.planSha256)"
            Write-Host "[test-local] run record: $recordPath"
        } catch { if ($null -eq $failure) { $failure = $_ } }
    }
}

if ($null -ne $failure) { throw $failure.Exception.Message }
