#!/usr/bin/env bash
set -euo pipefail

# Copies the agent skill(s) shipped in skills/ into the per-user skill
# directories of the common coding agents. Skill names are taken from the
# directory names, so this script does not need to know the tool's name.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

install_one() {
  local src="$1" dest_root="$2"
  local dest
  dest="${dest_root}/$(basename "${src}")"

  mkdir -p "${dest_root}"
  if command -v rsync >/dev/null 2>&1; then
    mkdir -p "${dest}"
    rsync -a --delete "${src}/" "${dest}/"
  else
    rm -rf "${dest}"
    cp -R "${src}" "${dest}"
  fi
  echo "Installed skill to: ${dest}"
}

found=0
for skill in "${ROOT}"/skills/*/; do
  skill="${skill%/}"
  [[ -f "${skill}/SKILL.md" ]] || continue
  found=1
  install_one "${skill}" "${HOME}/.agents/skills" # Codex, Gemini CLI, OpenCode
  install_one "${skill}" "${HOME}/.claude/skills" # Claude Code
done

if [[ "${found}" -eq 0 ]]; then
  echo "No skill found under ${ROOT}/skills" >&2
  exit 1
fi
