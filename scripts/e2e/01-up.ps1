#Requires -Version 7.0
. (Join-Path $PSScriptRoot 'common.ps1')

foreach ($command in 'docker', 'go', 'migrate') { Get-Command $command -ErrorAction Stop | Out-Null }
if (Test-Path -LiteralPath $StateFile) { throw 'An E2E run exists. Run 99-down.ps1 before starting another.' }
$socket = [Net.Sockets.TcpClient]::new()
try {
    if ($socket.ConnectAsync('127.0.0.1', 8080).Wait(500) -and $socket.Connected) {
        throw 'Port 8080 is already owned by another process.'
    }
} catch [AggregateException] {
    if ($_.Exception.InnerException -isnot [Net.Sockets.SocketException] -or
        $_.Exception.InnerException.SocketErrorCode -ne [Net.Sockets.SocketError]::ConnectionRefused) { throw }
} finally {
    $socket.Dispose()
}

$runId = [Guid]::NewGuid().ToString('N')
$run = [pscustomobject]@{
    Directory = Join-Path $StateDir $runId
    Override = ''; MySqlId = ''; DbPort = 0; Group = "go-server-e2e-$runId"
    Processes = [pscustomobject]@{ server = $null; publisher = $null; consumer = $null }
}
New-Item -ItemType Directory -Path $run.Directory -Force | Out-Null
$run.Override = Join-Path $run.Directory 'compose.e2e.yml'
@"
volumes:
  redis_data:
    name: go-server-e2e-redis-$runId
  kafka_data:
    name: go-server-e2e-kafka-$runId
"@ | Set-Content -LiteralPath $run.Override -Encoding UTF8
$script:ComposeOverride = $run.Override
Invoke-Compose config -q
$config = Invoke-Compose config --format json | ConvertFrom-Json
Observe "compose config exit=0; Redis volume=$($config.volumes.redis_data.name), Kafka volume=$($config.volumes.kafka_data.name)"
Save-Run $run
Observe "logs=$($run.Directory)"
Invoke-Compose up -d
Wait-ServiceHealthy redis
Wait-ServiceHealthy kafka -RepairKafkaVolume
Invoke-Compose ps

$password = [Guid]::NewGuid().ToString('N') + [Guid]::NewGuid().ToString('N')
$previous = $env:MYSQL_ROOT_PASSWORD
try {
    $env:MYSQL_ROOT_PASSWORD = $password
    $run.MySqlId = & docker run --rm -d --publish 127.0.0.1::3306 --env MYSQL_ROOT_PASSWORD `
        --env 'MYSQL_ROOT_HOST=%' --env "MYSQL_DATABASE=$DbName" mysql:8.0
    if ($LASTEXITCODE -ne 0 -or -not $run.MySqlId) { throw "docker run mysql exited $LASTEXITCODE" }
    Save-Run $run
} finally { $env:MYSQL_ROOT_PASSWORD = $previous }
$portLine = & docker port $run.MySqlId 3306/tcp
if ($LASTEXITCODE -ne 0 -or $portLine -notmatch '^127\.0\.0\.1:(\d+)$') { throw 'Could not resolve isolated MySQL port' }
$run.DbPort = [int]$Matches[1]
Save-Run $run
Observe "MySQL container=$($run.MySqlId.Substring(0, 12)) port=$($run.DbPort) database=$DbName"
$probe = 'MYSQL_PWD=$MYSQL_ROOT_PASSWORD exec mysql --protocol=tcp -h127.0.0.1 -uroot --batch --skip-column-names -e "SELECT 1"'
Wait-Until 'MySQL TCP initialization' 120 {
    $value = & docker exec $run.MySqlId sh -c $probe 2>$null
    return $LASTEXITCODE -eq 0 -and $value -eq '1'
} 1000 | Out-Null
Observe 'MySQL over TCP SELECT 1 -> 1'
Push-Location $RepoRoot
try {
    $migrationUrl = "mysql://root:$password@tcp(127.0.0.1:$($run.DbPort))/${DbName}?multiStatements=true"
    foreach ($action in 'up', 'version') {
        $output = & migrate -path migrations -database $migrationUrl $action 2>&1
        $code = $LASTEXITCODE
        $output | ForEach-Object { Write-Host ([string]$_).Replace($password, '[redacted]') }
        Observe "migrate $action exit=$code"
        if ($code -ne 0) { throw "migrate $action exited $code" }
    }
    foreach ($role in 'server', 'publisher', 'consumer') {
        $binary = Join-Path $run.Directory "$role.exe"
        & go build -o $binary "./cmd/$role"
        if ($LASTEXITCODE -ne 0) { throw "go build cmd/$role exited $LASTEXITCODE" }
        Observe "cmd/$role build bytes=$((Get-Item -LiteralPath $binary).Length)"
        Start-Role $run $role $password
        Wait-Role $run $role
    }
} finally { Pop-Location }
