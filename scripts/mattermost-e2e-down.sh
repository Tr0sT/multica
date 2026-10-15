#!/usr/bin/env bash
# Tear down the throwaway Mattermost server started by mattermost-e2e-up.sh.
# The container holds all of its own state, so removing it is a full cleanup.
set -euo pipefail

CONTAINER="${MM_E2E_CONTAINER:-mm-e2e}"

if docker inspect "$CONTAINER" >/dev/null 2>&1; then
  if [[ "$(docker inspect -f '{{index .Config.Labels "com.multica.mattermost-e2e"}}' "$CONTAINER")" != "true" ]]; then
    echo "refusing to remove ${CONTAINER}: not a Multica Mattermost E2E container" >&2
    exit 1
  fi
  docker rm -f "$CONTAINER" >/dev/null
  echo "removed ${CONTAINER}"
else
  echo "${CONTAINER} is not present"
fi
