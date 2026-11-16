#Requires -Version 7.0
param([string]$Owner = 'octocat', [string]$Repo = 'Spoon-Knife')
. (Join-Path $PSScriptRoot 'common.ps1')

function Get-SnapshotCounts($Run, [long]$Project, [long]$Job) {
    $sql = @"
SELECT JSON_OBJECT(
    'jobs', (SELECT COUNT(*) FROM import_jobs WHERE project_id = $Project),
    'files', (SELECT COUNT(*) FROM source_files WHERE import_job_id = $Job),
    'spans', (SELECT COUNT(*) FROM source_spans AS s JOIN source_files AS f ON f.id = s.source_file_id WHERE f.import_job_id = $Job));
"@
    Invoke-TestSql $Run $sql | ConvertFrom-Json
}

$rows = [Collections.Generic.List[hashtable]]::new()
try {
    $run = Open-FaultRun
    $scenario = New-Scenario $Owner $Repo
    $response = Complete-ApiRequest (Start-Import $scenario "replay-$([Guid]::NewGuid().ToString('N'))")
    Assert-Http $response 202 'create duplicate-event job'
    $job = [long]$response.Json.data.job_id
    $before = Wait-Import $run $job
    if ($before.status -ne 'succeeded' -or $before.outbox_rows -ne 1 -or -not $before.event) {
        throw "job=$job is not a successful import backed by exactly one outbox event"
    }
    $payloadRows = @(Invoke-TestSql $run "SELECT CAST(payload AS CHAR CHARACTER SET utf8mb4) FROM outbox_events WHERE job_id = $job AND event_type = 'ImportRequested' AND status = 'sent';")
    if ($payloadRows.Count -ne 1 -or -not $payloadRows[0]) { throw "missing sent outbox payload for job=$job" }
    $payload = $payloadRows[0]
    $countsBefore = Get-SnapshotCounts $run $scenario.Project $job
    if ($countsBefore.jobs -ne 1 -or $countsBefore.files -le 0 -or $countsBefore.spans -le 0) {
        throw "job=$job has no complete baseline: jobs=$($countsBefore.jobs) files=$($countsBefore.files) spans=$($countsBefore.spans)"
    }

    $log = $run.Processes.consumer.Log
    $received = 'received import event: event_id="{0}" job_id={1} ' -f $before.event, $job
    $skipped = 'job={0} already succeeded, skip duplicate event="{1}"' -f $job, $before.event
    $downloaded = "job=$job revision downloaded "
    $claimed = " claimed job=$job"
    $baseline = @(Wait-Until 'original Consumer delivery and terminal check' 20 {
        $lines = @(Get-Content -LiteralPath $log)
        if (($lines | Where-Object { $_.Contains($received) }) -and
            ($lines | Where-Object { $_.Contains($skipped) })) {
            return $lines
        }
    })
    $downloadsBefore = @($baseline | Where-Object { $_.Contains($downloaded) }).Count
    $claimsBefore = @($baseline | Where-Object { $_.EndsWith($claimed) }).Count
    if ($downloadsBefore -ne 1 -or $claimsBefore -ne $before.attempt) {
        throw "unexpected log baseline: downloads=$downloadsBefore claims=$claimsBefore attempts=$($before.attempt)"
    }
    Observe "job=$job event_id=$($before.event) payload_bytes=$([Text.Encoding]::UTF8.GetByteCount($payload)) log_baseline_lines=$($baseline.Count)"

    $payload | & docker exec -i go-kafka-compose /opt/kafka/bin/kafka-console-producer.sh `
        --bootstrap-server localhost:9092 --topic imports.requested --producer-property acks=all
    if ($LASTEXITCODE -ne 0) { throw "kafka-console-producer exited $LASTEXITCODE" }
    Observe 'kafka-console-producer exit=0'

    $replay = Wait-Until 'Consumer receives and skips the replayed event' 25 {
        $lines = @(Get-Content -LiteralPath $log | Select-Object -Skip $baseline.Count)
        $delivery = $lines | Where-Object { $_.Contains($received) } | Select-Object -First 1
        if ($delivery) {
            $skip = $lines | Select-Object -Skip ([Array]::IndexOf($lines, $delivery) + 1) |
                Where-Object { $_.Contains($skipped) } | Select-Object -First 1
            if ($skip) { return [pscustomobject]@{ Received = $delivery; Skipped = $skip } }
        }
    }
    Observe $replay.Received
    Observe $replay.Skipped

    $after = Get-Import $run $job
    $countsAfter = Get-SnapshotCounts $run $scenario.Project $job
    $allLines = @(Get-Content -LiteralPath $log)
    $downloadsAfter = @($allLines | Where-Object { $_.Contains($downloaded) }).Count
    $claimsAfter = @($allLines | Where-Object { $_.EndsWith($claimed) }).Count

    $rows.Add(@{ '检查项' = 'Consumer 日志'; '预期' = '同一 event ID 重新收到并 skip duplicate'; '实际' = $replay.Skipped })
    $rows.Add(@{ '检查项' = 'import_jobs 行数'; '预期' = '仍为 1'; '实际' = "$($countsBefore.jobs) → $($countsAfter.jobs)" })
    $rows.Add(@{ '检查项' = 'attempts'; '预期' = '不增加'; '实际' = "$($before.attempt) → $($after.attempt)" })
    $rows.Add(@{ '检查项' = '源码快照行数'; '预期' = '文件和 Span 均不增加'; '实际' = "files $($countsBefore.files) → $($countsAfter.files); spans $($countsBefore.spans) → $($countsAfter.spans)" })
    $rows.Add(@{ '检查项' = 'GitHub 导入调用'; '预期' = '无新的领取或下载日志'; '实际' = "claims $claimsBefore → $claimsAfter; revision downloaded $downloadsBefore → $downloadsAfter" })

    if ($after.status -ne 'succeeded' -or $countsAfter.jobs -ne 1 -or $after.attempt -ne $before.attempt -or
        $countsAfter.files -ne $countsBefore.files -or $countsAfter.spans -ne $countsBefore.spans -or
        $claimsAfter -ne $claimsBefore -or $downloadsAfter -ne $downloadsBefore) {
        throw "replayed event changed import execution or persisted data for job=$job"
    }
} catch {
    $rows.Add(@{ '检查项' = '执行结果'; '预期' = '消费端幂等校验通过'; '实际' = $_.Exception.Message })
    throw
} finally {
    Show-Table $rows @('检查项', '预期', '实际')
    Close-FaultRun
}
