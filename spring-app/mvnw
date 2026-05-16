#!/usr/bin/env sh
set -e

BASE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

WRAPPER_DIR="$BASE_DIR/.mvn/wrapper"
WRAPPER_JAR="$WRAPPER_DIR/maven-wrapper.jar"
WRAPPER_PROPERTIES="$WRAPPER_DIR/maven-wrapper.properties"

if [ ! -f "$WRAPPER_JAR" ]; then
  echo "Maven Wrapper jar not found: $WRAPPER_JAR" >&2
  echo "Please download it (Windows): powershell -ExecutionPolicy Bypass -File \"$WRAPPER_DIR/download-maven-wrapper.ps1\"" >&2
  exit 1
fi

exec java -classpath "$WRAPPER_JAR" -Dmaven.multiModuleProjectDirectory="$BASE_DIR" org.apache.maven.wrapper.MavenWrapperMain "$@"
