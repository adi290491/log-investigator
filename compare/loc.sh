#!/usr/bin/env bash
# Counts non-blank, non-comment lines in Go and Python files.
#
# Usage: compare/loc.sh PATH...
#   PATH is a file or a directory. Directories are searched recursively for
#   *.go and *.py; *_test.go, test_*.py and .venv/ are skipped. Files passed
#   explicitly are always counted.
#
# Comments: Go // lines and /* */ blocks; Python # lines. Python docstrings are
# counted as code, because MCP SDKs use them as tool descriptions (the Go
# equivalent is the Description string, which is also counted).
set -euo pipefail

if [ $# -eq 0 ]; then
  sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
fi

count() {
  case "$1" in
    *.go)
      awk '
        { line = $0 }
        inblock { if (sub(/.*\*\//, "", line)) inblock = 0; else next }
        { gsub(/\/\*.*\*\//, "", line) }
        line ~ /\/\*/ { sub(/\/\*.*/, "", line); inblock = 1 }
        { sub(/^[ \t]+/, "", line) }
        line == "" || line ~ /^\/\// { next }
        { n++ }
        END { print n + 0 }' "$1" ;;
    *.py)
      awk '{ sub(/^[ \t]+/, "") } $0 == "" || /^#/ { next } { n++ } END { print n + 0 }' "$1" ;;
    *) echo 0 ;;
  esac
}

total=0
files=()
for p in "$@"; do
  if [ -d "$p" ]; then
    while IFS= read -r f; do files+=("$f"); done < <(
      find "$p" -name .venv -prune -o -type f \( -name '*.go' -o -name '*.py' \) \
        ! -name '*_test.go' ! -name 'test_*.py' -print | sort)
  elif [ -f "$p" ]; then
    files+=("$p")
  else
    echo "loc.sh: no such file or directory: $p" >&2
    exit 1
  fi
done

for f in "${files[@]}"; do
  n=$(count "$f")
  total=$((total + n))
  printf '%6d  %s\n' "$n" "$f"
done
printf '%6d  total\n' "$total"
