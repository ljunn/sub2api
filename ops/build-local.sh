#!/usr/bin/env bash
set -euo pipefail

source_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
runtime_dir=/opt/sub2api
cd "$source_dir"
# Preview and production must include every pending source change. Do not
# provide a dirty-worktree bypass that silently leaves part of the release out.
if [[ $# != 0 ]]; then
  echo 'Usage: ./ops/build-local.sh (no partial-build options)' >&2
  exit 1
fi
if [[ -n $(git status --porcelain) ]]; then
  echo 'Commit and push ALL pending source changes before building the complete release.' >&2
  exit 1
fi
commit=$(git rev-parse HEAD)
remote_head=$(git ls-remote --exit-code origin refs/heads/host-production | cut -f1)
if [[ "$remote_head" != "$commit" ]]; then
  echo 'Push the complete committed HEAD to origin/host-production before building.' >&2
  exit 1
fi
version=$(tr -d '\r\n' < backend/cmd/server/VERSION)
release_dir="$runtime_dir/releases/${version}-${commit:0:12}"
mkdir -p "$runtime_dir/releases"
if [[ -e "$release_dir" ]]; then
  (cd "$release_dir" && sha256sum -c SHA256SUMS)
  echo "$release_dir"
  exit 0
fi
stage=$(mktemp -d "$runtime_dir/releases/.build-${commit:0:12}.XXXXXX")
echo "Building committed source $commit in $stage"
mkdir "$stage/source"
git archive "$commit" | tar -x -C "$stage/source"
(
  cd "$stage/source/frontend"
  npx --yes pnpm@9.15.9 install --frozen-lockfile
  npx --yes pnpm@9.15.9 run build
)
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
(
  cd "$stage/source/backend"
  GOMAXPROCS=4 CGO_ENABLED=0 go build -p 2 -tags embed -trimpath \
    -ldflags="-s -w -X main.Commit=$commit -X main.Date=$build_date -X main.BuildType=source" \
    -o "$stage/sub2api" ./cmd/server
)
"$stage/sub2api" -version
python3 - "$stage" "$commit" "$version" "$build_date" <<'PY'
import json, pathlib, sys
stage, commit, version, built_at = sys.argv[1:]
manifest = dict(commit=commit, version=version, built_at=built_at, build_type="source",
                source_directory="/opt/sub2api/source", repository="https://github.com/ljunn/sub2api",
                branch="host-production", pnpm="9.15.9", frontend_embedded=True)
pathlib.Path(stage, "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
PY
(cd "$stage" && sha256sum sub2api manifest.json > SHA256SUMS)
# This directory was created above and contains only this build's git archive.
rm -rf -- "$stage/source"
chmod 755 "$stage" "$stage/sub2api"
chmod 644 "$stage/manifest.json" "$stage/SHA256SUMS"
if [[ $(git rev-parse HEAD) != "$commit" || -n $(git status --porcelain) ]]; then
  echo 'Source changed during the build. Integrate, test, commit and push all changes, then rebuild.' >&2
  exit 1
fi
mv -- "$stage" "$release_dir"
echo "$release_dir"
