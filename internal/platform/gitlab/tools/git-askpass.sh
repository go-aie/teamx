#!/bin/sh
set -eu
exec "${TEAMX_CLI:?TeamX CLI unavailable}" workspace-git askpass --binding "$(dirname "$0")/binding.json" "${1:-}"
