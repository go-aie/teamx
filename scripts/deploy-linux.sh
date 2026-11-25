#!/bin/sh
set -eu
TEAMX_SCRIPT_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
exec node "$TEAMX_SCRIPT_ROOT/scripts/deploy-linux.mjs" "$@"
