#!/usr/bin/env bash
set -euo pipefail
source_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
[[ -L /opt/sub2api/previous ]] || { echo 'No previous release recorded.' >&2; exit 1; }
exec "$source_dir/ops/activate-release.sh" "$(readlink -f /opt/sub2api/previous)"
