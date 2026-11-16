#Requires -Version 7.0
. (Join-Path $PSScriptRoot 'common.ps1')

if (-not (Test-Path -LiteralPath $StateFile)) {
    Observe 'No E2E state file; no managed resources recorded'
    return
}
$run = Get-Run
$failures = [Collections.Generic.List[string]]::new()
foreach ($role in 'consumer', 'publisher', 'server') {
    $process = Get-OwnedProcess $run $role
    if (-not $process) {
        Observe "$role process not present"
        continue
    }
    try {
        Stop-Process -Id $process.Id -Force
        if (-not $process.WaitForExit(5000)) { throw "$role pid=$($process.Id) did not exit" }
        Observe "$role pid=$($process.Id) exited"
    } catch { $failures.Add($_.Exception.Message) }
}
try {
    if ($run.Override) { Invoke-Compose down --volumes }
    else { Invoke-Compose down }
    Invoke-Compose ps
    Observe "compose service states: redis=$(Get-ServiceState redis), kafka=$(Get-ServiceState kafka)"
} catch { $failures.Add($_.Exception.Message) }
if ($run.MySqlId) {
    $running = & docker container ls -aq --filter "id=$($run.MySqlId)"
    if ($LASTEXITCODE -ne 0) { $failures.Add('Could not inspect the isolated MySQL container') }
    elseif ($running) {
        & docker stop $run.MySqlId
        if ($LASTEXITCODE -ne 0) { $failures.Add("Could not stop isolated MySQL $($run.MySqlId)") }
        else { Observe "isolated MySQL $($run.MySqlId.Substring(0, 12)) stopped (--rm)" }
    }
    else { Observe "isolated MySQL $($run.MySqlId.Substring(0, 12)) absent" }
}
Observe "logs retained at $($run.Directory)"
if ($failures.Count -ne 0) { throw ($failures -join '; ') }
Remove-Item -LiteralPath $StateFile
