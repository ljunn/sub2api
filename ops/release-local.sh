#!/usr/bin/env bash
set -euo pipefail

source_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$source_dir"
commit=$("$source_dir/ops/require-main.sh")
"$source_dir/ops/build-local.sh"
[[ $("$source_dir/ops/require-main.sh") == "$commit" ]] || {
  echo 'The main commit changed during the build. Review and rebuild the complete version.' >&2; exit 1;
}
version=$(tr -d '\r\n' < backend/cmd/server/VERSION)
exec "$source_dir/ops/activate-release.sh" "/opt/sub2api/releases/${version}-${commit:0:12}"
