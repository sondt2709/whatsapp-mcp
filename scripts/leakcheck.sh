#!/usr/bin/env bash
# Search all tracked and staged files for forbidden terms.
# The terms are in .leakcheck.local, one term per line. That file is not committed,
# because the terms themselves are private (local paths, names, server addresses).
# Lines that are empty or start with # are ignored. The search ignores case.
# go.sum holds only module paths and hashes; its base64 hashes match short terms by chance.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
terms_file=".leakcheck.local"

if [[ ! -f "$terms_file" ]]; then
  echo "leakcheck: $terms_file not found, nothing to check"
  exit 0
fi

patterns=$(mktemp)
trap 'rm -f "$patterns"' EXIT
grep -v -e '^[[:space:]]*$' -e '^[[:space:]]*#' "$terms_file" > "$patterns" || true

if [[ ! -s "$patterns" ]]; then
  echo "leakcheck: $terms_file has no terms"
  exit 0
fi

files=$( { git ls-files; git diff --cached --name-only --diff-filter=ACMR; git ls-files --others --exclude-standard; } | sort -u | grep -v -x -e go.sum)
found=0
while IFS= read -r f; do
  [[ -f "$f" ]] || continue
  if grep -n -i -F -f "$patterns" -- "$f" >/dev/null 2>&1; then
    echo "leakcheck: forbidden term in $f:"
    grep -n -i -F -o -f "$patterns" -- "$f" | head -5 | sed 's/^/  line /'
    found=1
  fi
done <<< "$files"

if [[ $found -ne 0 ]]; then
  echo "leakcheck: FAILED"
  exit 1
fi
echo "leakcheck: OK ($(wc -l < "$patterns" | tr -d ' ') terms, $(wc -l <<< "$files" | tr -d ' ') files)"
