"""GUI-launched adversarial cleanup fixture: detached and ignores SIGTERM."""

import os
from pathlib import Path
import pwd
import signal
import time

if os.fork():
    raise SystemExit(0)
os.setsid()
if os.fork():
    raise SystemExit(0)
signal.signal(signal.SIGTERM, signal.SIG_IGN)
home = Path(pwd.getpwuid(os.getuid()).pw_dir)
(home / 'gui-background.pid').write_text(str(os.getpid()))
while True:
    (home / 'gui-background.heartbeat').write_text(str(time.time()))
    time.sleep(0.2)
