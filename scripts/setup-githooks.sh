#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"

git config core.hooksPath .githooks

# Ensure hooks are executable.
if [[ -d .githooks ]]; then
  chmod +x .githooks/* 2>/dev/null || true
fi

echo "Configured git hooksPath to .githooks"
