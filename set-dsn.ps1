param(
    [string]$DbHost = "127.0.0.1",
    [int]$DbPort = 3307,
    [string]$DbUser = "root"
)

# 安全输入密码
$securePassword = Read-Host "请输入 MySQL 密码" -AsSecureString
$ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($securePassword)

try {
    $dbPassword = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
}
finally {
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
}

$base = "${DbUser}:${dbPassword}@tcp(${DbHost}:${DbPort})"
$options = "charset=utf8mb4&parseTime=true&loc=UTC&time_zone=%27%2B00%3A00%27"

# 业务数据库
$env:MYSQL_DSN = "${base}/go_db?${options}"

# 测试数据库
$env:TEST_MYSQL_DSN = "${base}/go_server_test?${options}"

Remove-Variable dbPassword, base -ErrorAction SilentlyContinue

Write-Host "MYSQL_DSN 已设置：go_db"
Write-Host "TEST_MYSQL_DSN 已设置：go_server_test"