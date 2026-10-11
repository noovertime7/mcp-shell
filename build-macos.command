#!/bin/bash
set -e
cd "$(dirname "$0")"
bash ./build-gui-macos.sh
printf '\n构建完成。请解压 dist 内的 macOS ZIP 并打开 MCP Shell.app。\n'
