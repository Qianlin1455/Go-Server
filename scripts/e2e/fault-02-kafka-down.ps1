#Requires -Version 7.0
param([string]$Owner = 'octocat', [string]$Repo = 'Spoon-Knife')
. (Join-Path $PSScriptRoot 'common.ps1')

$rows = [Collections.Generic.List[hashtable]]::new()
$needsRestart = $false
try {
    $run = Open-FaultRun
    $scenario = New-Scenario $Owner $Repo
    Wait-ServiceHealthy kafka
    $needsRestart = $true
    Invoke-Compose stop kafka
    $state = Get-ServiceState kafka
    Observe "Kafka after stop=$state"
    if ($state -notlike 'exited/*') { throw "expected stopped Kafka, got $state" }

    $jobs = @()
    $tag = [Guid]::NewGuid().ToString('N')
    for ($index = 1; $index -le 3; $index++) {
        $result = Complete-ApiRequest (Start-Import $scenario "offline-$tag-$index")
        Observe "Kafka stopped; POST #$index HTTP=$($result.Status) code=$($result.Json.code) job=$($result.Json.data.job_id) status=$($result.Json.data.status)"
        $jobs += [pscustomobject]@{ Id = [long]$result.Json.data.job_id; Status = $result.Status }
    }
    $rows.Add(@{ '检查项' = 'Broker 停机期间创建'; '预期' = '3 × HTTP 202'; '实际' = "Kafka=$state; HTTP=$($jobs.Status -join ','), jobs=$($jobs.Id -join ',')" })
    if (@($jobs | Where-Object { $_.Status -ne 202 -or $_.Id -le 0 }).Count -ne 0) { throw 'API did not accept all three jobs while Kafka was stopped' }

    $pending = Wait-Until 'publisher failure while Kafka stopped' 90 {
        $snapshots = @($jobs | ForEach-Object { Get-Import $run $_.Id })
        if (@($snapshots | Where-Object { $_.outbox -ne 'pending' -or $_.status -ne 'queued' }).Count -ne 0) {
            throw "unexpected job/outbox while Kafka stopped: $(($snapshots | ForEach-Object { "$($_.id)=$($_.status)/$($_.outbox)" }) -join ', ')"
        }
        if (@($snapshots | Where-Object { $_.publish_errors -gt 0 -and $_.outbox_error }).Count -eq 3) {
            return $snapshots
        }
    } 1000
    foreach ($snapshot in $pending) {
        Observe "job=$($snapshot.id) status=$($snapshot.status) outbox=$($snapshot.outbox) failures=$($snapshot.publish_errors) error=$($snapshot.outbox_error)"
    }
    $summary = $pending | ForEach-Object { "$($_.id)=$($_.status)/$($_.outbox) (failures=$($_.publish_errors))" }
    $rows.Add(@{ '检查项' = '停机期间 Outbox'; '预期' = '三条 pending，publisher 记录失败'; '实际' = $summary -join ', ' })

    $restarting = [DateTimeOffset]::UtcNow
    Invoke-Compose start kafka
    Wait-ServiceHealthy kafka
    $needsRestart = $false
    foreach ($job in $jobs) {
        $current = Wait-Import $run $job.Id 150
        $publishDelay = $null
        if ($current.sent_epoch_ms) { $publishDelay = [long]$current.sent_epoch_ms - $restarting.ToUnixTimeMilliseconds() }
        $rows.Add(@{ '检查项' = "恢复后 job $($job.Id)"; '预期' = '自动补发 → sent，任务 succeeded'; '实际' = "job=$($current.status), outbox=$($current.outbox), start→sent=${publishDelay}ms, outbox_rows=$($current.outbox_rows), error=$($current.error)" })
    }
    foreach ($job in $jobs) {
        $current = Get-Import $run $job.Id
        if ($current.status -ne 'succeeded' -or $current.outbox -ne 'sent' -or $current.outbox_rows -ne 1) { throw 'One or more jobs did not recover after Kafka restarted' }
    }
} catch {
    $rows.Add(@{ '检查项' = '执行结果'; '预期' = '停机可写且恢复补发'; '实际' = $_.Exception.Message })
    throw
} finally {
    Show-Table $rows @('检查项', '预期', '实际')
    try {
        if ($needsRestart -and -not $script:OwnsFaultStack) {
            Invoke-Compose start kafka
            Wait-ServiceHealthy kafka
        }
    } finally { Close-FaultRun }
}
