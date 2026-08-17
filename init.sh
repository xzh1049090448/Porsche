#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

echo "==> 当前目录: $PWD"
echo "==> 检查 Go 工具链"
go version

echo "==> 同步依赖"
go mod tidy

echo "==> 运行基础验证"
go test ./...

echo "==> 启动命令"
echo "    go run ./cmd/server"

if [ "${RUN_START_COMMAND:-0}" = "1" ]; then
  echo "==> 启动应用"
  exec go run ./cmd/server
fi

echo "如果希望 init.sh 直接启动应用，请设置 RUN_START_COMMAND=1。"
