$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$script:RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$sha = [Security.Cryptography.SHA256]::Create()
try {
    $slug = [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($RepoRoot))).Replace('-', '').Substring(0, 12)
} finally {
    $sha.Dispose()
}
$script:StateDir = Join-Path ([IO.Path]::GetTempPath()) "go-server-e2e-$slug"
$script:StateFile = Join-Path $StateDir 'state.json'
$script:DbName = 'go_server_e2e'
$script:Api = 'http://127.0.0.1:8080'
Add-Type -AssemblyName System.Net.Http
$handler = [Net.Http.HttpClientHandler]::new()
$handler.UseProxy = $false
$handler.AllowAutoRedirect = $false
$script:Http = [Net.Http.HttpClient]::new($handler)
$Http.Timeout = [TimeSpan]::FromSeconds(16)

function Observe([string]$Message) {
    Write-Host "[$([DateTimeOffset]::Now.ToString('o'))] $Message"
}

function Invoke-Compose {
    $files = @('-f', (Join-Path $RepoRoot 'compose.yml'))
    if ($script:ComposeOverride) { $files += @('-f', $script:ComposeOverride) }
    & docker compose --project-directory $RepoRoot @files @args
    if ($LASTEXITCODE -ne 0) { throw "docker compose $($args[0]) exited $LASTEXITCODE" }
}

function Wait-Until([string]$Label, [int]$Seconds, [scriptblock]$Check, [int]$IntervalMs = 500) {
    $watch = [Diagnostics.Stopwatch]::StartNew()
    do {
        $result = & $Check
        if ($result) { return $result }
        Start-Sleep -Milliseconds $IntervalMs
    } while ($watch.Elapsed.TotalSeconds -lt $Seconds)
    throw "$Label timed out after ${Seconds}s"
}

function Get-ServiceState([string]$Service) {
    $id = Invoke-Compose ps -a -q $Service
    if (-not $id) { return 'missing' }
    $value = & docker inspect --format '{{.State.Status}}/{{.State.Health.Status}}' $id
    if ($LASTEXITCODE -ne 0) { throw "docker inspect $Service exited $LASTEXITCODE" }
    return "$value"
}

function Wait-ServiceHealthy([string]$Service, [switch]$RepairKafkaVolume) {
    $last = ''
    $repaired = $false
    Wait-Until "$Service health" 150 {
        $current = Get-ServiceState $Service
        if ($current -ne $last) {
            Observe "$Service state=$current"
            Set-Variable -Name last -Value $current -Scope 1
        }
        if ($current -eq 'running/healthy') { return $true }
        if ($RepairKafkaVolume -and -not $repaired -and $current -ne 'missing') {
            $log = @(Invoke-Compose logs --tail 12 $Service)
            if (($log -join "`n") -match 'AccessDeniedException: /tmp/kraft-combined-logs/') {
                Observe 'Kafka KRaft volume AccessDeniedException; assigning the mounted directory to image UID:GID 1000:1000'
                Invoke-Compose stop kafka | Out-Host
                Invoke-Compose run --rm --no-deps --user 0 --entrypoint chown kafka 1000:1000 /tmp/kraft-combined-logs | Out-Host
                Invoke-Compose start kafka | Out-Host
                Set-Variable -Name repaired -Value $true -Scope 1
            }
        }
    } | Out-Null
}

function Save-Run($Run) {
    $Run | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $StateFile -Encoding UTF8
}

function Get-Run {
    if (-not (Test-Path -LiteralPath $StateFile -PathType Leaf)) {
        throw 'Run 01-up.ps1 before running the scenarios.'
    }
    $run = Get-Content -LiteralPath $StateFile -Raw | ConvertFrom-Json
    $script:ComposeOverride = $run.Override
    return $run
}

function Open-FaultRun {
    $script:OwnsFaultStack = -not (Test-Path -LiteralPath $StateFile)
    if ($script:OwnsFaultStack) {
        & (Join-Path $PSScriptRoot '01-up.ps1') | Out-Host
    }
    Get-Run
}

function Close-FaultRun {
    if ($script:OwnsFaultStack) { & (Join-Path $PSScriptRoot '99-down.ps1') }
}

function Invoke-TestSql($Run, [string]$Sql) {
    $mysql = 'MYSQL_PWD=$MYSQL_ROOT_PASSWORD exec mysql --protocol=socket -uroot --database=go_server_e2e --batch --raw --skip-column-names'
    $rows = $Sql | & docker exec -i $Run.MySqlId sh -c $mysql
    if ($LASTEXITCODE -ne 0) { throw "Test MySQL query exited $LASTEXITCODE" }
    return $rows
}

function Get-OwnedProcess($Run, [string]$Role) {
    $entry = $Run.Processes.PSObject.Properties[$Role].Value
    if (-not $entry) { return }
    $process = Get-Process -Id $entry.Id -ErrorAction SilentlyContinue
    if ($process -and $process.Path -eq $entry.Path -and $process.StartTime.ToUniversalTime().Ticks -eq [long]$entry.StartTicks) {
        return $process
    }
}

function Start-Role($Run, [string]$Role, [string]$Password) {
    $binary = Join-Path $Run.Directory "$Role.exe"
    $logBase = Join-Path $Run.Directory "$Role-$([DateTime]::UtcNow.Ticks)"
    $settings = @{
        MYSQL_DSN = "root:$Password@tcp(127.0.0.1:$($Run.DbPort))/${DbName}?parseTime=true&loc=UTC"
        JWT_SECRET = [Guid]::NewGuid().ToString('N') + [Guid]::NewGuid().ToString('N')
        REDIS_ADDR = '127.0.0.1:6379'; REDIS_PASSWORD = ''
        KAFKA_BROKERS = '127.0.0.1:9092'; KAFKA_GROUP_ID = $Run.Group
        AI_BASE_URL = ''; AI_MODEL = ''; AI_API_KEY = ''; DEBUG_POOL = ''
        PUBLISHER_INTERVAL = '1s'; PUBLISHER_BATCH_SIZE = ''
    }
    $previous = @{}
    foreach ($key in $settings.Keys) { $previous[$key] = [Environment]::GetEnvironmentVariable($key, 'Process') }
    try {
        foreach ($key in $settings.Keys) { [Environment]::SetEnvironmentVariable($key, $settings[$key], 'Process') }
        $process = Start-Process -FilePath $binary -WorkingDirectory $RepoRoot -PassThru -WindowStyle Hidden `
            -RedirectStandardOutput "$logBase.out" -RedirectStandardError "$logBase.log"
        $Run.Processes.$Role = [pscustomobject]@{
            Id = $process.Id; StartTicks = "$($process.StartTime.ToUniversalTime().Ticks)"
            Path = $binary; Log = "$logBase.log"; Output = "$logBase.out"
        }
        Save-Run $Run
        Observe "$Role pid=$($process.Id), stderr=$logBase.log"
    } finally {
        foreach ($key in $previous.Keys) { [Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process') }
    }
}

function Wait-Role($Run, [string]$Role) {
    $ready = @{ publisher = 'publisher started'; consumer = 'consumer starting' }
    Wait-Until "$Role startup" 30 {
        if (-not (Get-OwnedProcess $Run $Role)) { throw "$Role exited; see $($Run.Processes.$Role.Log)" }
        if ($Role -eq 'server') {
            try { return (Invoke-Api GET '/healthz').Status -eq 200 } catch { return $false }
        }
        $log = Get-Content -LiteralPath $Run.Processes.$Role.Log -Raw -ErrorAction SilentlyContinue
        return $log -match $ready[$Role]
    } | Out-Null
    if ($Role -eq 'server') { Observe 'server GET /healthz HTTP=200' }
    else { Observe "$Role startup log matched '$($ready[$Role])'" }
}

function Start-ApiRequest([string]$Method, [string]$Path, $Body, [string]$Token, [string]$Key) {
    $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::new($Method), "$Api$Path")
    if ($Token) { $request.Headers.Authorization = [Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $Token) }
    if ($Key) { $request.Headers.Add('Idempotency-Key', $Key) }
    if ($null -ne $Body) {
        $request.Content = [Net.Http.StringContent]::new(($Body | ConvertTo-Json -Compress), [Text.Encoding]::UTF8, 'application/json')
    }
    try {
        return [pscustomobject]@{ Request = $request; Task = $Http.SendAsync($request) }
    } catch {
        $request.Dispose()
        throw
    }
}

function Complete-ApiRequest($Pending) {
    try {
        $response = $Pending.Task.GetAwaiter().GetResult()
        try {
            $content = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            $json = $null
            if ($content -and $response.Content.Headers.ContentType.MediaType -eq 'application/json') {
                $json = $content | ConvertFrom-Json
            }
            return [pscustomobject]@{ Status = [int]$response.StatusCode; Json = $json; At = [DateTimeOffset]::Now }
        } finally { $response.Dispose() }
    } finally { $Pending.Request.Dispose() }
}

function Invoke-Api([string]$Method, [string]$Path, $Body, [string]$Token, [string]$Key) {
    return Complete-ApiRequest (Start-ApiRequest $Method $Path $Body $Token $Key)
}

function Assert-Http($Response, [int]$Expected, [string]$Step) {
    Observe "$Step HTTP=$($Response.Status) code=$($Response.Json.code)"
    if ($Response.Status -ne $Expected) { throw "$Step expected HTTP $Expected, got $($Response.Status) $($Response.Json.code)" }
}

function New-Scenario([string]$Owner, [string]$Repo, [scriptblock]$OnStep = {}) {
    $tag = [Guid]::NewGuid().ToString('N')
    $email = "e2e-$tag@example.com"
    $password = [Guid]::NewGuid().ToString('N')
    $result = Invoke-Api POST '/api/v1/auth/register' @{ username = "e2e-$tag"; email = $email; password = $password }
    Assert-Http $result 201 'register'
    & $OnStep 'register' "HTTP 201 user=$($result.Json.data.id)"
    $result = Invoke-Api POST '/api/v1/auth/login' @{ email = $email; password = $password }
    Assert-Http $result 200 'login'
    $token = $result.Json.data.access_token
    if (-not $token) { throw 'login returned no access_token' }
    & $OnStep 'login' 'HTTP 200 JWT received'
    $result = Invoke-Api POST '/api/v1/projects' @{ name = "Kafka E2E $tag" } $token
    Assert-Http $result 201 'create project'
    $project = [long]$result.Json.data.id
    if ($project -le 0) { throw 'create project returned no ID' }
    & $OnStep 'create project' "HTTP 201 project=$project"
    $result = Invoke-Api POST "/api/v1/projects/$project/repositories" @{ owner = $Owner; repo = $Repo } $token
    Assert-Http $result 201 "bind $Owner/$Repo"
    $repository = [long]$result.Json.data.id
    if ($repository -le 0) { throw 'bind returned no ID' }
    Observe "project=$project repository=$repository branch=$($result.Json.data.default_branch) SHA=$($result.Json.data.last_sha)"
    & $OnStep 'bind repository' "HTTP 201 $Owner/$Repo repository=$repository"
    return [pscustomobject]@{ Project = $project; Repository = $repository; Token = $token }
}

function Start-Import($Scenario, [string]$Key) {
    return Start-ApiRequest POST "/api/v1/projects/$($Scenario.Project)/imports" `
        @{ repository_id = $Scenario.Repository } $Scenario.Token $Key
}

function Get-Import($Run, [long]$Job) {
    $sql = @"
SELECT JSON_OBJECT('id', j.id, 'status', j.status, 'attempt', j.attempt_count,
    'error', j.last_error_code, 'owner', j.lease_owner, 'lease', j.lease_expires_at,
    'first_ms', TIMESTAMPDIFF(MICROSECOND, j.created_at, j.started_at) DIV 1000,
    'last_at', j.last_attempt_at, 'end_ms', TIMESTAMPDIFF(MICROSECOND, j.created_at, j.finished_at) DIV 1000,
    'work_ms', TIMESTAMPDIFF(MICROSECOND, j.started_at, j.finished_at) DIV 1000,
    'sent_ms', TIMESTAMPDIFF(MICROSECOND, j.created_at, o.sent_at) DIV 1000,
    'sent_epoch_ms', CAST(UNIX_TIMESTAMP(o.sent_at) * 1000 AS UNSIGNED),
    'outbox', o.status, 'publish_errors', o.attempts, 'outbox_error', o.last_error, 'event', o.event_id,
    'outbox_rows', (SELECT COUNT(*) FROM outbox_events WHERE job_id = j.id))
FROM import_jobs AS j LEFT JOIN outbox_events AS o ON o.job_id = j.id AND o.event_type = 'ImportRequested'
WHERE j.id = $Job;
"@
    $rows = @(Invoke-TestSql $Run $sql)
    if ($rows.Count -ne 1) { throw "expected one import job $Job, got $($rows.Count)" }
    return $rows[0] | ConvertFrom-Json
}

function Wait-Import($Run, [long]$Job, [int]$Seconds = 150, [scriptblock]$OnChange = {}) {
    $last = ''
    return Wait-Until "job $Job completion" $Seconds {
        $current = Get-Import $Run $Job
        $key = "$($current.status)/$($current.outbox)/$($current.attempt)"
        if ($key -ne $last) {
            Observe "job=$Job status=$($current.status) outbox=$($current.outbox) attempts=$($current.attempt) last_error=$($current.error)"
            & $OnChange $current
            Set-Variable -Name last -Value $key -Scope 1
        }
        if ($current.status -eq 'failed') { return $current }
        if ($current.status -eq 'succeeded' -and $current.outbox -eq 'sent') { return $current }
    }
}

function Show-Table($Rows, [string[]]$Headers) {
    Write-Host ('| ' + ($Headers -join ' | ') + ' |')
    Write-Host ('| ' + ((@('---') * $Headers.Count) -join ' | ') + ' |')
    foreach ($row in $Rows) {
        $cells = foreach ($header in $Headers) {
            ([string]$row[$header]).Replace('|', '\|') -replace '[\r\n]+', ' '
        }
        Write-Host ('| ' + ($cells -join ' | ') + ' |')
    }
}
