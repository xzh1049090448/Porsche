$ErrorActionPreference = "Stop"

$projectBaseDir = Split-Path -Parent $PSScriptRoot
$wrapperDir = Join-Path $projectBaseDir ".mvn\wrapper"
$jarPath = Join-Path $wrapperDir "maven-wrapper.jar"

if (Test-Path $jarPath) {
  Write-Host "maven-wrapper.jar already exists."
  exit 0
}

New-Item -ItemType Directory -Force -Path $wrapperDir | Out-Null

$url = "https://repo.maven.apache.org/maven2/org/apache/maven/wrapper/maven-wrapper/3.3.2/maven-wrapper-3.3.2.jar"

Write-Host "Downloading Maven Wrapper jar..."
Invoke-WebRequest -Uri $url -OutFile $jarPath
Write-Host "Saved to $jarPath"
