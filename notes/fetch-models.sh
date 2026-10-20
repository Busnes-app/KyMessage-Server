#!/bin/sh
# usage: fetch-models.sh MODELS.txt DIR. Downloads each file at its pinned revision into
# DIR/<repo>/<path> and fails on any checksum mismatch. Build stage only.
set -eu
grep -v '^#' "$1" | while read -r repo rev path sum; do
	mkdir -p "$2/$repo/$(dirname "$path")"
	curl -fsSL --proto '=https' --retry 3 -o "$2/$repo/$path" "https://huggingface.co/$repo/resolve/$rev/$path"
	echo "$sum  $2/$repo/$path" | sha256sum -c -
done
