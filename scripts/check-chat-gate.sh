#!/usr/bin/env bash
# The unreviewed chat client stays out of the deployed console and image until the
# independent review passes. Lifting this gate is a deliberate, reviewed edit.
set -u
root=$(git rev-parse --show-toplevel)
web=$root/web
fail=0
# grep exits 2 on an unreadable or missing path; that is a failure, never a clean scan.
found() {
  grep "$@"
  case $? in
    0) return 0 ;;
    1) return 1 ;;
    *) echo "gate: could not scan: $*"; fail=1; return 1 ;;
  esac
}
# Any quoted specifier naming the chat code: import, require, src=, alias or tsconfig path.
if found -rEnI --exclude-dir=node_modules --exclude-dir=dist --exclude-dir=browser --exclude="*.md" \
  "[\"'\`][^\"'\`]*(chat-core|mls-proof|ts-mls)" "$web"; then
  echo "gate: web/ must not reference chat-core, mls-proof or ts-mls"; fail=1
fi
if found -Eq 'chat-core|mls-proof|ts-mls' "$web/package.json"; then
  echo "gate: web/package.json must not depend on chat-core, mls-proof or ts-mls"; fail=1
fi
# The built artifact itself: ts-mls labels every derivation with this RFC 9420 prefix.
if found -rqF 'MLS 1.0 ' "$web/dist"; then
  echo "gate: web/dist contains ts-mls code"; fail=1
fi
if ! grep -qx '/chat-core/' "$root/.dockerignore"; then
  echo "gate: .dockerignore must exclude /chat-core/"; fail=1
fi
if [ ! -f "$root/chat-core/package.json" ]; then
  echo "gate: chat-core/ package missing"; fail=1
fi
exit $fail
