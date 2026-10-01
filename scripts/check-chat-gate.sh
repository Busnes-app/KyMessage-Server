#!/usr/bin/env bash
# The unreviewed chat client stays out of the deployed console and image until the
# independent review passes. Lifting this gate is a deliberate, reviewed edit.
set -u
root=$(git rev-parse --show-toplevel)
fail=0
if grep -rEn "chat-core|from ['\"]ts-mls" "$root/web/src" "$root/web/vite.config.ts" 2>/dev/null; then
  echo "gate: web/ must not import chat-core or ts-mls"; fail=1
fi
if grep -q '"ts-mls"' "$root/web/package.json"; then
  echo "gate: web/package.json must not list ts-mls"; fail=1
fi
if ! grep -qx '/chat-core/' "$root/.dockerignore"; then
  echo "gate: .dockerignore must exclude /chat-core/"; fail=1
fi
if [ ! -f "$root/chat-core/package.json" ]; then
  echo "gate: chat-core/ package missing"; fail=1
fi
exit $fail
