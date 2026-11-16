#!/usr/bin/env sh
set -eu

SOURCE="$(CDPATH= cd -- "$(dirname -- "$0")/../skills/aihub-deploy" && pwd)"
if [ -n "${AIHUB_SKILLS_DIR:-}" ]; then
  TARGET="$AIHUB_SKILLS_DIR/aihub-deploy"
else
  TARGET="${HOME}/.aihub/skills/aihub-deploy"
fi
mkdir -p "$TARGET"
cp -R "$SOURCE"/. "$TARGET"/
printf 'Installed AI Hub deploy skill to %s\n' "$TARGET"
