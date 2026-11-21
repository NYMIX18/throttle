#!/usr/bin/env bash
# Usage: ./scripts/rename-module.sh github.com/<you>/throttle
# Replaces the placeholder module path everywhere (go.mod, imports, docs).
set -euo pipefail
cd "$(dirname "$0")/.."
new="${1:?usage: $0 github.com/<you>/throttle}"
old="github.com/your-username/throttle"
grep -rl --include='*.go' --include='go.mod' --include='*.md' --include='*.yml' "$old" . \
  | xargs sed -i.bak "s#$old#$new#g"
find . -name '*.bak' -delete
echo "Module renamed to $new. Now run: make fmt && make check"
