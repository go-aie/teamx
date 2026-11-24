#!/bin/sh
# Test-only path adapter. Never installed as a TeamX launcher or setuid helper.
# The caller must explicitly approve this extra host privilege for the experiment.
set -eu
test "$(id -u)" = 0
test "$#" = 1
experiment=$1
case "$experiment" in
  /root/luop/teamx/srt-isolation-*) ;;
  *) echo 'refuse path outside the dedicated experiment prefix' >&2; exit 2 ;;
esac
test "$(realpath -e -- "$experiment")" = "$experiment"
test -f "$experiment/native-entry.sh"
test "$(stat -c %u "$experiment/native-entry.sh")" = 0
# Recursive private propagation confines the bind mount to this process tree.
# /mnt already exists; this creates no host file or mount outside the new namespace.
exec unshare --mount --propagation private /bin/sh -eu -c '
  mount --bind "$1" /mnt
  cd /mnt
  exec setpriv --reuid=nobody --regid=nogroup --clear-groups \
    --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs \
    /usr/bin/env -i HOME=/mnt/lab/a/home TMPDIR=/mnt/lab/a/tmp \
    PATH=/mnt/node/bin:/mnt/tools/usr/bin:/usr/bin:/bin \
    /bin/sh /mnt/native-entry.sh
' sh "$experiment"
