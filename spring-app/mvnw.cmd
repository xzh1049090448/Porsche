@REM ----------------------------------------------------------------------------
@REM Maven Wrapper startup script for Windows
@REM ----------------------------------------------------------------------------
@echo off
setlocal

set "MAVEN_PROJECTBASEDIR=%~dp0"
if "%MAVEN_PROJECTBASEDIR:~-1%"=="\" set "MAVEN_PROJECTBASEDIR=%MAVEN_PROJECTBASEDIR:~0,-1%"

set "MVNW_VERBOSE=false"

set "WRAPPER_DIR=%MAVEN_PROJECTBASEDIR%\.mvn\wrapper"
set "WRAPPER_JAR=%WRAPPER_DIR%\maven-wrapper.jar"
set "WRAPPER_PROPERTIES=%WRAPPER_DIR%\maven-wrapper.properties"

if not exist "%WRAPPER_JAR%" (
  echo Maven Wrapper jar not found: "%WRAPPER_JAR%"
  echo Please run: powershell -ExecutionPolicy Bypass -File "%WRAPPER_DIR%\download-maven-wrapper.ps1"
  exit /b 1
)

set "JAVA_EXE=java"

for /f "tokens=1,* delims==" %%A in ('findstr /r /c:"^distributionUrl=" "%WRAPPER_PROPERTIES%"') do (
  set "MAVEN_WRAPPER_DISTRIBUTION_URL=%%B"
)

if "%MAVEN_WRAPPER_DISTRIBUTION_URL%"=="" (
  echo distributionUrl not set in "%WRAPPER_PROPERTIES%"
  exit /b 1
)

set "MAVEN_OPTS=%MAVEN_OPTS%"

set "MAVEN_CMD_LINE_ARGS=%*"

"%JAVA_EXE%" %MAVEN_OPTS% -classpath "%WRAPPER_JAR%" -Dmaven.multiModuleProjectDirectory="%MAVEN_PROJECTBASEDIR%" org.apache.maven.wrapper.MavenWrapperMain %MAVEN_CMD_LINE_ARGS%
exit /b %ERRORLEVEL%
