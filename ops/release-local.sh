#!/usr/bin/env bash
set -euo pipefail

source_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$source_dir"
[[ $(git branch --show-current) == host-production ]] || {
  echo 'Production releases must use the host-production branch.' >&2; exit 1;
}
[[ -z $(git status --porcelain) ]] || { echo 'Commit all source changes first.' >&2; exit 1; }
git fetch origin host-production
commit=$(git rev-parse HEAD)
git merge-base --is-ancestor "$commit" FETCH_HEAD || {
  echo 'Push this commit to origin/host-production before deployment.' >&2; exit 1;
}
"$source_dir/ops/build-local.sh"
version=$(tr -d '\r\n' < backend/cmd/server/VERSION)
exec "$source_dir/ops/activate-release.sh" "/opt/sub2api/releases/${version}-${commit:0:12}"
