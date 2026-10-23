param()
$ErrorActionPreference = 'Stop'
if (-not $env:TEST_MYSQL_DSN -or $env:TEST_MYSQL_DSN -notmatch '/go_server_test(?:\?|$)') {
    throw 'Set TEST_MYSQL_DSN with an authorized account for go_server_test. Never use go_db.'
}
$originalRuntimeDSN = $env:MYSQL_DSN
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path
Push-Location $repoRoot
try {
    # Middleware integration tests use MYSQL_DSN rather than TEST_MYSQL_DSN.
    $env:MYSQL_DSN = $env:TEST_MYSQL_DSN
    go test -p 1 ./internal/repository ./internal/service ./internal/handler -count=1
    if ($LASTEXITCODE -ne 0) { throw 'Layer tests failed' }
    go test -p 1 ./... -count=1
    if ($LASTEXITCODE -ne 0) { throw 'Full tests failed' }
    go test -p 1 ./internal/service -run '^TestChangeStatus_DoneRollbackWhenAuditFails$' -count=1 -v
    if ($LASTEXITCODE -ne 0) { throw 'Transaction injection test failed' }
} finally {
    $env:MYSQL_DSN = $originalRuntimeDSN
    Pop-Location
}
