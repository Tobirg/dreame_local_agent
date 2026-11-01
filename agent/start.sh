#!/bin/sh
PATH=/usr/sbin:/usr/bin:/sbin:/bin:/ava/bin:$PATH
export PATH
BASE=/data/dreame-local
BIN=$BASE/dreame-local-agent
PIDFILE=/tmp/dreame-local-agent.pid
LOG=/tmp/dreame-local-agent.log
umask 077
if [ -r "$PIDFILE" ]; then
    read -r pid < "$PIDFILE"
    case "$pid" in ''|*[!0-9]*) ;; *)
        exe=$(readlink "/proc/$pid/exe" 2>/dev/null)
        if [ "$exe" = "$BIN" ] && kill -0 "$pid" 2>/dev/null; then
            exit 0
        fi
        ;;
    esac
fi
[ -x "$BIN" ] && [ -r "$BASE/config.json" ] || exit 1
exec "$BIN" --daemon --config "$BASE/config.json"
