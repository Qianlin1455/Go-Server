#Requires -Version 7.0
param([string]$Owner = 'octocat', [string]$Repo = 'Spoon-Knife')
. (Join-Path $PSScriptRoot 'common.ps1')

Get-Run | Out-Null
$rows = [Collections.Generic.List[hashtable]]::new()
function Add-Stage([string]$Phase, [string]$Note, [string]$Status = '-', [string]$Outbox = '-') {
    $rows.Add(@{
        '阶段' = $Phase; '时间戳' = [DateTimeOffset]::Now.ToString('o')
        'job status' = $Status; 'outbox status' = $Outbox; '备注' = $Note
    })
}

$watch = [Diagnostics.Stopwatch]::StartNew()
try {
    $scenario = New-Scenario $Owner $Repo { param($step, $note) Add-Stage $step $note }
    $createWatch = [Diagnostics.Stopwatch]::StartNew()
    $result = Complete-ApiRequest (Start-Import $scenario "e2e-$([Guid]::NewGuid().ToString('N'))")
    Assert-Http $result 202 'create import'
    $job = [long]$result.Json.data.job_id
    $revision = $result.Json.data.revision_sha
    if ($job -le 0 -or $revision -notmatch '^[a-f0-9]{40}$') { throw 'create import returned an invalid job ID or revision' }
    Add-Stage 'create import' "HTTP 202 job=$job SHA=$revision" $result.Json.data.status

    $path = "/api/v1/imports/$job"
    $lastState = ''
    $current = Wait-Until "GET $path terminal status" 150 {
        $response = Invoke-Api GET $path $null $scenario.Token
        if ($response.Status -ne 200 -or $response.Json.code -ne 'SUCCESS') {
            throw "GET $path HTTP=$($response.Status) code=$($response.Json.code)"
        }
        $state = $response.Json.data
        if ([long]$state.id -ne $job -or $state.revision_sha -ne $revision) {
            throw "GET $path returned a different job ID or revision"
        }
        if ($state.status -notin 'queued', 'running', 'succeeded', 'failed' -or $null -eq $state.attempts) {
            throw "GET $path returned invalid job status or attempts"
        }
        $key = "$($state.status)/$($state.attempts)/$($state.last_error_code)"
        if ($key -ne $lastState) {
            $stage = if ($lastState) { 'GET import poll' } else { 'GET import probe' }
            $note = "HTTP $($response.Status) job=$job attempts=$($state.attempts) error=$($state.last_error_code)"
            Observe "GET $path status=$($state.status) $note"
            Add-Stage $stage $note $state.status
            Set-Variable -Name lastState -Value $key -Scope 1
        }
        if ($state.status -in 'succeeded', 'failed') { return $state }
    }
    if ($current.status -ne 'succeeded') { throw "job $job failed: $($current.last_error_code)" }
    $persistedMs = ([DateTimeOffset]$current.updated_at - [DateTimeOffset]$current.created_at).TotalMilliseconds
    Add-Stage 'import terminal' "API created_at→updated_at=${persistedMs}ms, POST→terminal GET=$($createWatch.ElapsedMilliseconds)ms" $current.status

    $files = Invoke-Api GET "/api/v1/projects/$($scenario.Project)/source-files?revision_sha=$revision&import_job_id=$job" $null $scenario.Token
    Assert-Http $files 200 'get imported source files'
    $data = $files.Json.data
    $first = $data.items | Select-Object -First 1
    if ([long]$data.import_job_id -ne $job -or -not $first -or $first.revision_sha -ne $revision) {
        throw "source files did not verify the imported snapshot for job $job"
    }
    Add-Stage 'GET source-files' "HTTP 200 job=$job total=$($data.total) first=$($first.path) scenario=$($watch.ElapsedMilliseconds)ms" $current.status
} catch {
    Add-Stage 'error' $_.Exception.Message
    throw
} finally {
    Show-Table $rows @('阶段', '时间戳', 'job status', 'outbox status', '备注')
}
