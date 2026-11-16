#Requires -Version 7.0
param([string]$Owner = 'octocat', [string]$Repo = 'Spoon-Knife')
. (Join-Path $PSScriptRoot 'common.ps1')

$rows = [Collections.Generic.List[hashtable]]::new()
try {
    $run = Open-FaultRun
    $scenario = New-Scenario $Owner $Repo
    $key = "duplicate-$([Guid]::NewGuid().ToString('N'))"
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $requests = @(1..10 | ForEach-Object { Start-Import $scenario $key })
    $results = @(
        foreach ($request in $requests) {
            try { Complete-ApiRequest $request }
            catch { [pscustomobject]@{ Status = 0; Json = $null; At = [DateTimeOffset]::Now } }
        }
    )
    $distribution = ($results | Group-Object Status | Sort-Object Name | ForEach-Object { "$($_.Name)×$($_.Count)" }) -join ', '
    $codes = ($results | Group-Object { $_.Json.code } | ForEach-Object { "$($_.Name)×$($_.Count)" }) -join ', '
    $returnedIds = @($results | Where-Object { $_.Status -in 200, 202 } | ForEach-Object { [long]$_.Json.data.job_id } | Sort-Object -Unique)
    Observe "concurrent=10 HTTP=[$distribution] codes=[$codes] response_job_ids=$($returnedIds -join ',') duration=$($watch.ElapsedMilliseconds)ms"
    $rows.Add(@{ '检查项' = '并发 HTTP'; '预期' = '1×202，其余 200 或 409'; '实际' = "$distribution; $codes; duration=$($watch.ElapsedMilliseconds)ms" })

    $jobs = @(Invoke-TestSql $run "SELECT id FROM import_jobs WHERE project_id = $($scenario.Project) AND idempotency_key = '$key' ORDER BY id;")
    $rows.Add(@{ '检查项' = '数据库 job 数'; '预期' = '1，所有成功响应返回同一 ID'; '实际' = "rows=$($jobs.Count), DB IDs=$($jobs -join ','), response IDs=$($returnedIds -join ',')" })
    if ($jobs.Count -ne 1) { throw 'The database did not contain exactly one job for the key' }
    $job = [long]$jobs[0]
    $state = Wait-Import $run $job 150
    $rows.Add(@{ '检查项' = '导入与 Outbox'; '预期' = '一次执行、一个事件、最终 succeeded'; '实际' = "status=$($state.status), attempts=$($state.attempt), outbox=$($state.outbox), outbox_rows=$($state.outbox_rows), error=$($state.error)" })
    $created = @($results | Where-Object Status -eq 202)
    $unexpected = @($results | Where-Object { $_.Status -notin 200, 202, 409 })
    if ($created.Count -ne 1 -or $unexpected.Count -ne 0 -or $returnedIds.Count -ne 1 -or $returnedIds[0] -ne $job -or
        $state.status -ne 'succeeded' -or $state.attempt -ne 1 -or $state.outbox_rows -ne 1) {
        throw 'Duplicate-request observations differ from the expected idempotency'
    }
} catch {
    $rows.Add(@{ '检查项' = '执行结果'; '预期' = '幂等校验通过'; '实际' = $_.Exception.Message })
    throw
} finally {
    Show-Table $rows @('检查项', '预期', '实际')
    Close-FaultRun
}
