# Security hardening smoke checks (run while ai-gateway is on http://127.0.0.1:8000)
param(
    [string]$BaseUrl = "http://127.0.0.1:8000",
    [string]$AdminToken = "change-me-for-dev-only"
)

function Get-HttpStatus {
    param(
        [string]$Method,
        [string]$Uri,
        [hashtable]$Headers = @{},
        [string]$Body = $null
    )
    try {
        $params = @{
            Uri             = $Uri
            Method          = $Method
            Headers         = $Headers
            UseBasicParsing = $true
        }
        if ($Body) {
            $params.ContentType = "application/json"
            $params.Body = $Body
        }
        $resp = Invoke-WebRequest @params
        return [int]$resp.StatusCode
    } catch {
        if ($_.Exception.Response -and $_.Exception.Response.StatusCode) {
            return [int]$_.Exception.Response.StatusCode.value__
        }
        throw
    }
}

function Test-Check {
    param(
        [string]$Name,
        [int]$ExpectedStatus,
        [scriptblock]$Run
    )
    try {
        $status = & $Run
        $pass = ($status -eq $ExpectedStatus)
        if ($pass) {
            Write-Host "[PASS] $Name ($status)" -ForegroundColor Green
        } else {
            Write-Host "[FAIL] $Name (expected $ExpectedStatus, got $status)" -ForegroundColor Red
        }
        return $pass
    } catch {
        Write-Host "[FAIL] $Name - $($_.Exception.Message)" -ForegroundColor Red
        return $false
    }
}

Write-Host "Security verification against $BaseUrl"

$allOk = $true

$allOk = (Test-Check -Name "metrics denied without token" -ExpectedStatus 401 {
    Get-HttpStatus -Method GET -Uri "$BaseUrl/metrics"
}) -and $allOk

$allOk = (Test-Check -Name "metrics allowed with admin token" -ExpectedStatus 200 {
    Get-HttpStatus -Method GET -Uri "$BaseUrl/metrics" -Headers @{ Authorization = "Bearer $AdminToken" }
}) -and $allOk

$allOk = (Test-Check -Name "platform models denied without jwt" -ExpectedStatus 401 {
    Get-HttpStatus -Method GET -Uri "$BaseUrl/api/v1/platform/models"
}) -and $allOk

$phone = "13800138666"
$body = (@{ phone = $phone } | ConvertTo-Json)
for ($i = 1; $i -le 5; $i++) {
    $null = Get-HttpStatus -Method POST -Uri "$BaseUrl/api/v1/auth/send-code" -Body $body
}

$allOk = (Test-Check -Name "sms rate limit on 6th send" -ExpectedStatus 429 {
    Get-HttpStatus -Method POST -Uri "$BaseUrl/api/v1/auth/send-code" -Body $body
}) -and $allOk

if ($allOk) {
    Write-Host "`nAll security checks passed." -ForegroundColor Green
    exit 0
}
Write-Host "`nSome security checks failed." -ForegroundColor Yellow
exit 1
