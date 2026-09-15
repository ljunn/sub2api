#!/usr/bin/env bash
set -euo pipefail

runtime_dir=/opt/sub2api
target=${1:?Usage: activate-release.sh /opt/sub2api/releases/VERSION-COMMIT}
target=$(realpath -e -- "$target")
[[ "$target" == "$runtime_dir/releases/"* && -x "$target/sub2api" ]] || {
  echo 'The target must be a built release under /opt/sub2api/releases.' >&2; exit 1;
}
(cd "$target" && sha256sum -c SHA256SUMS)
exec 9>"$runtime_dir/.release.lock"
flock -n 9 || { echo 'Another release is running.' >&2; exit 1; }

if [[ -L "$runtime_dir/current" ]]; then
  previous=$(readlink -f "$runtime_dir/current")
else
  # Preserve the installed release binary before the first source deployment.
  legacy_sha=$(sha256sum "$runtime_dir/sub2api" | cut -d ' ' -f 1)
  previous="$runtime_dir/releases/legacy-${legacy_sha:0:12}"
  mkdir -p "$previous"
  if [[ ! -e "$previous/sub2api" ]]; then
    cp -p -- "$runtime_dir/sub2api" "$previous/sub2api"
    (cd "$previous" && sha256sum sub2api > SHA256SUMS)
  fi
fi

set_current() {
  ln -s -- "$1" "$runtime_dir/.current-next-$$"
  mv -Tf -- "$runtime_dir/.current-next-$$" "$runtime_dir/current"
  if [[ ! -L "$runtime_dir/sub2api" ]] || [[ $(readlink "$runtime_dir/sub2api") != current/sub2api ]]; then
    ln -s current/sub2api "$runtime_dir/.binary-next-$$"
    mv -Tf -- "$runtime_dir/.binary-next-$$" "$runtime_dir/sub2api"
  fi
}

healthy() {
  local expected=$1 pid
  for ((attempt=0; attempt<30; attempt++)); do
    pid=$(systemctl show sub2api -p MainPID --value)
    if [[ "$pid" != 0 && $(readlink -f "/proc/$pid/exe") == "$expected/sub2api" ]] && \
       curl -fsS --max-time 2 http://127.0.0.1:7654/health >/dev/null; then
      return 0
    fi
    sleep 1
  done
  return 1
}

set_current "$target"
if systemctl restart sub2api && healthy "$target"; then
  ln -s -- "$previous" "$runtime_dir/.previous-next-$$"
  mv -Tf -- "$runtime_dir/.previous-next-$$" "$runtime_dir/previous"
  printf '%s %s previous=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$target" "$previous" >> "$runtime_dir/release-history.log"
  echo "Active: $target"
else
  echo "Health check failed; restoring $previous" >&2
  set_current "$previous"
  systemctl restart sub2api
  healthy "$previous" || { echo 'Rollback health check failed; inspect journalctl -u sub2api.' >&2; exit 1; }
  exit 1
fi
