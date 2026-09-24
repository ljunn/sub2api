#!/usr/bin/env bash
set -euo pipefail

source_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$source_dir"
[[ -z $(git status --porcelain) ]] || {
  echo 'Commit and push ALL pending source changes before building or releasing.' >&2; exit 1;
}
branch=$(git branch --show-current)
[[ "$branch" == main || -z "$branch" ]] || {
  echo 'Builds and releases must use main or a detached checkout of the same main commit.' >&2; exit 1;
}
git fetch --quiet origin refs/heads/main:refs/remotes/origin/main
commit=$(git rev-parse HEAD)
local_main=$(git rev-parse refs/heads/main)
remote_main=$(git rev-parse refs/remotes/origin/main)
[[ "$commit" == "$local_main" && "$commit" == "$remote_main" ]] || {
  echo 'HEAD, local main and freshly fetched origin/main must be identical.' >&2; exit 1;
}
printf '%s\n' "$commit"
