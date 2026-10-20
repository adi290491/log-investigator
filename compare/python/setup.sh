#!/usr/bin/env bash
# Creates compare/python/.venv with the pinned requirements.
# Uses uv if installed, else python3 -m venv + pip.
set -euo pipefail
cd "$(dirname "$0")"
if command -v uv >/dev/null 2>&1; then
  uv venv -q --allow-existing --python "$(cat .python-version)" .venv
  uv pip install -q -p .venv -r requirements.txt
else
  python3 -m venv .venv
  .venv/bin/pip install -q -r requirements.txt
fi
echo "python env ready: compare/python/.venv ($(.venv/bin/python -V))"
