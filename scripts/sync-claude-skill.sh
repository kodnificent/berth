#!/bin/sh
# The frontmatter lives only in SKILL.md; its body is always the working guide.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
skill=$root/plugins/claude/skills/working-in-a-berth/SKILL.md
guide=$root/docs/agents/working-in-a-berth.md
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
awk 'NR == 1 && $0 != "---" { exit 1 } { print } NR > 1 && $0 == "---" { found = 1; exit } END { exit !found }' "$skill" >"$tmp" ||
	{ echo "$skill has no frontmatter between --- lines" >&2; exit 1; }
printf '\n' >>"$tmp"
cat "$guide" >>"$tmp"
cat "$tmp" >"$skill"
