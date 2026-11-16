#Requires -Version 7.0
param([string]$Owner = 'octocat', [string]$Repo = 'Spoon-Knife')
. (Join-Path $PSScriptRoot 'common.ps1')

$rows = [Collections.Generic.List[hashtable]]::new()
$killed = $null
try {
    $run = Open-FaultRun
    $worker = Get-OwnedProcess $run consumer
    if (-not $worker) { throw 'The managed consumer is not running' }
    $scenario = New-Scenario $Owner $Repo
    $response = Complete-ApiRequest (Start-Import $scenario "kill-$([Guid]::NewGuid().ToString('N'))")
    Assert-Http $response 202 'create worker-kill job'
    $job = [long]$response.Json.data.job_id
    $before = Wait-Until "job $job running" 60 {
        $snapshot = Get-Import $run $job
        if ($snapshot.status -eq 'running') { return $snapshot }
        if ($snapshot.status -in 'succeeded', 'failed') { throw "job $job reached $($snapshot.status) before running was sampled" }
    } 50
    if ($before.owner -ne "consumer-worker-$($worker.Id)") { throw "unexpected lease owner=$($before.owner)" }
    Observe "job=$job running lease_owner=$($before.owner) lease_expires_utc=$($before.lease) attempt=$($before.attempt) outbox=$($before.outbox)"
    $killRequested = [DateTimeOffset]::UtcNow
    & taskkill.exe /F /PID $worker.Id
    if ($LASTEXITCODE -ne 0 -or -not $worker.WaitForExit(5000)) { throw "taskkill did not stop consumer pid=$($worker.Id)" }
    $killed = [DateTimeOffset]::UtcNow
    $afterKill = Get-Import $run $job
    $stillRunning = $afterKill.status -eq 'running' -and $afterKill.owner -eq $before.owner -and $afterKill.attempt -eq $before.attempt
    $rows.Add(@{ '检查项' = '强制退出后'; '预期' = '旧 lease 仍处于 running'; '实际' = "pid=$($worker.Id) exited, job=$job status=$($afterKill.status), attempt=$($afterKill.attempt), taskkill=$([int]($killed - $killRequested).TotalMilliseconds)ms" })

    $password = & docker exec $run.MySqlId printenv MYSQL_ROOT_PASSWORD
    if ($LASTEXITCODE -ne 0 -or -not $password) { throw 'Could not read the isolated MySQL credential' }
    Start-Role $run consumer $password
    Wait-Role $run consumer
    $final = Wait-Import $run $job 150
    $observedMs = [int]([DateTimeOffset]::UtcNow - $killed).TotalMilliseconds
    $styles = [Globalization.DateTimeStyles]::AssumeUniversal
    $culture = [Globalization.CultureInfo]::InvariantCulture
    $lease = [DateTimeOffset]::Parse($before.lease, $culture, $styles)
    $reclaimed = [DateTimeOffset]::Parse($final.last_at, $culture, $styles)
    $afterLeaseMs = [int]($reclaimed - $lease).TotalMilliseconds
    $afterKillMs = [int]($reclaimed - $killed).TotalMilliseconds
    $sameEvent = $final.event -eq $before.event
    $rows.Add(@{ '检查项' = '重新领取'; '预期' = '60s lease 到期后，attempt 递增'; '实际' = "kill→claim=${afterKillMs}ms, lease expiry→claim=${afterLeaseMs}ms, attempt $($before.attempt)→$($final.attempt)" })
    $rows.Add(@{ '检查项' = '最终状态'; '预期' = 'succeeded'; '实际' = "$($final.status), kill→terminal observation=${observedMs}ms, error=$($final.error)" })
    $rows.Add(@{ '检查项' = 'Outbox'; '预期' = '同一条记录及 event ID'; '实际' = "rows=$($final.outbox_rows), status=$($final.outbox), same_event=$sameEvent" })
    if (-not $stillRunning -or $final.status -ne 'succeeded' -or $final.attempt -ne ($before.attempt + 1) -or $afterLeaseMs -lt 0 -or $final.outbox_rows -ne 1 -or -not $sameEvent) {
        throw 'Worker-kill observations differ from the expected recovery'
    }
} catch {
    $rows.Add(@{ '检查项' = '执行结果'; '预期' = '恢复完成'; '实际' = $_.Exception.Message })
    throw
} finally {
    Show-Table $rows @('检查项', '预期', '实际')
    try {
        if ($killed -and -not $script:OwnsFaultStack -and -not (Get-OwnedProcess $run consumer)) {
            $restorePassword = & docker exec $run.MySqlId printenv MYSQL_ROOT_PASSWORD
            if ($LASTEXITCODE -eq 0 -and $restorePassword) {
                Start-Role $run consumer $restorePassword
                Wait-Role $run consumer
            } else { Observe 'Could not restore the managed consumer; see 01-up/99-down' }
        }
    } finally { Close-FaultRun }
}
