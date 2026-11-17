#!/usr/bin/env bash
set -Eeuo pipefail
export PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
umask 077

# The wrapper holds the lock without leaking its descriptor into the daemon.
if [[ "${MULTICA_FORK_UPDATE_LOCKED:-}" != 1 ]]; then
  exec /usr/bin/python3 - "$0" "$@" <<'PY'
import fcntl, os, pathlib, subprocess, sys
lock = open(pathlib.Path.home() / '.multica/fork-update.lock', 'a')
try:
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
except BlockingIOError:
    print('Multica update is already running.')
    sys.exit(0)
result = subprocess.run(['/bin/bash', *sys.argv[1:]], env={**os.environ, 'MULTICA_FORK_UPDATE_LOCKED': '1'})
sys.exit(result.returncode)
PY
fi
unset MULTICA_FORK_UPDATE_LOCKED

exec >> "$HOME/multica_update.log" 2>&1
echo "$(date -u) Updating Multica from Tr0sT/multica main"
server_dir="$HOME/.multica/server"
source_dir="$HOME/.multica/fork-source"
cli_link=/usr/local/bin/multica
fork_bin_dir=/usr/local/lib/multica-fork
repo=https://github.com/Tr0sT/multica.git

[[ -f "$server_dir/.env" && -f "$server_dir/docker-compose.selfhost.yml" ]]
[[ -L "$cli_link" && -w /usr/local/bin && -w /usr/local/lib ]]
if [[ ! -e "$source_dir" ]]; then
  git clone --quiet --single-branch --branch main "$repo" "$source_dir"
fi
[[ "$(git -C "$source_dir" remote get-url origin)" == "$repo" ]]
[[ -z "$(git -C "$source_dir" status --porcelain)" ]] || { echo 'Fork checkout has local changes; refusing to overwrite them.'; exit 1; }
git -C "$source_dir" fetch --quiet origin main
git -C "$source_dir" checkout --quiet --detach FETCH_HEAD
export MULTICA_FORK_SOURCE="$source_dir"
export MULTICA_FORK_COMMIT="$(git -C "$source_dir" rev-parse HEAD)"
export MULTICA_FORK_VERSION="$(git -C "$source_dir" describe --tags --long --always)"
compose=(docker compose --project-name multica --env-file "$server_dir/.env"
  -f "$server_dir/docker-compose.selfhost.yml" -f "$source_dir/deploy/nord/docker-compose.fork.yml")

wait_service() {
  local port attempt
  port="$("${compose[@]}" port "$1" "$2")"
  port="${port##*:}"
  [[ "$port" =~ ^[0-9]+$ ]]
  for attempt in {1..60}; do
    if curl -fsS --max-time 5 "http://127.0.0.1:$port$3" -o /dev/null; then return 0; fi
    sleep 2
  done
  echo "Health check failed: $1"
  return 1
}

switched=0
daemon_stopped=0
cli_switched=0
backup_dir=''
old_cli_target="$(readlink "$cli_link")"
rollback() {
  local status=$1
  trap - ERR
  set +e
  echo "Update failed (exit $status). Backup: $backup_dir"
  if [[ "$switched" == 1 ]]; then
    "${compose[@]}" -f "$backup_dir/rollback.yml" up -d --no-build --pull never --no-deps backend frontend
  fi
  if [[ "$cli_switched" == 1 ]]; then
    "$cli_link" daemon stop
    ln -s "$old_cli_target" "$cli_link.rollback.$$"
    mv -f "$cli_link.rollback.$$" "$cli_link"
  fi
  if [[ "$daemon_stopped" == 1 ]]; then
    "$cli_link" daemon start --no-auto-update
  fi
  exit "$status"
}
trap 'rollback "$?"' ERR

deploy() {
  local current backend_id frontend_id builder_image native_dir stamp daemon_state was_running active attempt
  current="$(docker inspect --format '{{.Config.Image}}' "$("${compose[@]}" ps -q backend)")"
  if [[ "$current" == "multica-fork-backend:$MULTICA_FORK_COMMIT" ]] &&
      "$cli_link" --version | grep -Fq "$MULTICA_FORK_COMMIT"; then
    wait_service backend 8080 /health
    wait_service frontend 3000 /
    echo "Already deployed: $MULTICA_FORK_COMMIT"
    return
  fi

  # Build everything before stopping the daemon or replacing running containers.
  "${compose[@]}" build backend
  builder_image="multica-fork-builder:$MULTICA_FORK_COMMIT"
  docker build --target builder -t "$builder_image" \
    --build-arg "VERSION=$MULTICA_FORK_VERSION" --build-arg "COMMIT=$MULTICA_FORK_COMMIT" "$source_dir"
  native_dir="$(mktemp -d "$HOME/.multica/fork-cli.XXXXXX")"
  docker run --rm --entrypoint /bin/sh -v "$native_dir:/out" \
    -e "VERSION=$MULTICA_FORK_VERSION" -e "COMMIT=$MULTICA_FORK_COMMIT" \
    -e GOOS=darwin -e GOARCH=amd64 "$builder_image" -ec \
    'cd /src/server; CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.buildCommit=$COMMIT" -o /out/multica ./cmd/multica'
  "$native_dir/multica" --version | grep -F "$MULTICA_FORK_COMMIT"
  "${compose[@]}" build frontend

  daemon_state="$("$cli_link" daemon status --output json)"
  active="$(printf '%s' "$daemon_state" | /usr/bin/python3 -c 'import json,sys; s=json.load(sys.stdin); print(s.get("active_task_count",0)+s.get("pending_terminal_report_count",0))')"
  if [[ "$active" != 0 ]]; then
    echo 'Daemon has active work; retry on the next scheduled update.'
    rm -f "$native_dir/multica"
    rmdir "$native_dir"
    return
  fi
  was_running="$(printf '%s' "$daemon_state" | /usr/bin/python3 -c 'import json,sys; print(json.load(sys.stdin).get("status") == "running")')"

  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  backup_dir="$HOME/.multica/backups/fork-$stamp"
  mkdir -p "$backup_dir"
  cp "$server_dir/.env" "$server_dir/docker-compose.selfhost.yml" "$backup_dir/"
  cp "$HOME/upgrade_multica.sh" "$backup_dir/upgrade_multica.sh"
  cp "$cli_link" "$backup_dir/multica"
  cp "$HOME/.multica/config.json" "$backup_dir/config.json"
  backend_id="$(docker inspect --format '{{.Image}}' "$("${compose[@]}" ps -q backend)")"
  frontend_id="$(docker inspect --format '{{.Image}}' "$("${compose[@]}" ps -q frontend)")"
  docker image tag "$backend_id" "multica-fork-backend:rollback-$stamp"
  docker image tag "$frontend_id" "multica-fork-web:rollback-$stamp"
  printf 'services:\n  backend:\n    image: %s\n  frontend:\n    image: %s\n' "$backend_id" "$frontend_id" > "$backup_dir/rollback.yml"
  printf '%s\n' "$old_cli_target" > "$backup_dir/cli-symlink-target"
  "${compose[@]}" exec -T postgres pg_dump -U multica -d multica -Fc > "$backup_dir/database.dump"
  "${compose[@]}" exec -T postgres pg_restore --list < "$backup_dir/database.dump" > "$backup_dir/database.contents"
  "${compose[@]}" exec -T backend tar -czf - -C /app/data/uploads . > "$backup_dir/uploads.tar.gz"
  chmod -R go-rwx "$backup_dir"

  if [[ "$was_running" == True ]]; then
    daemon_stopped=1
    "$cli_link" daemon stop
    for attempt in {1..60}; do
      daemon_state="$("$cli_link" daemon status --output json)"
      if printf '%s' "$daemon_state" | /usr/bin/python3 -c 'import json,sys; sys.exit(json.load(sys.stdin).get("status") != "stopped")'; then break; fi
      sleep 2
    done
    printf '%s' "$daemon_state" | /usr/bin/python3 -c 'import json,sys; assert json.load(sys.stdin).get("status") == "stopped"'
  fi
  switched=1
  "${compose[@]}" up -d --no-build --pull never --no-deps backend frontend
  wait_service backend 8080 /health
  wait_service frontend 3000 /
  "${compose[@]}" exec -T backend /app/multica --version | grep -F "$MULTICA_FORK_COMMIT"
  install -d -m 755 "$fork_bin_dir"
  install -m 755 "$native_dir/multica" "$fork_bin_dir/multica-$MULTICA_FORK_COMMIT"
  rm -f "$native_dir/multica"
  rmdir "$native_dir"
  ln -s "$fork_bin_dir/multica-$MULTICA_FORK_COMMIT" "$cli_link.next.$$"
  mv -f "$cli_link.next.$$" "$cli_link"
  cli_switched=1
  if [[ "$was_running" == True ]]; then
    "$cli_link" daemon start --no-auto-update
    "$cli_link" daemon status --output json | /usr/bin/python3 -c 'import json,sys; assert json.load(sys.stdin).get("status") == "running"'
  fi
  # Replace atomically: the executing shell continues reading the old inode.
  install -m 755 "$source_dir/deploy/nord/upgrade_multica.sh" "$HOME/upgrade_multica.sh.next"
  mv -f "$HOME/upgrade_multica.sh.next" "$HOME/upgrade_multica.sh"
  switched=0
  daemon_stopped=0
  cli_switched=0
  echo "Deployed $MULTICA_FORK_COMMIT; backup $backup_dir"
}

deploy
# Keep the existing independent Codex update, without making it a Multica rollback trigger.
if brew upgrade --cask codex; then
  codex_dir="$(brew --caskroom codex)"
  /usr/bin/xattr -dr com.apple.quarantine "$codex_dir" || true
else
  echo 'Codex update failed; Multica deployment is unaffected.'
fi
