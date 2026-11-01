#!/bin/sh
BASE=/data/dreame-local
PIDFILE=/tmp/dreame-local-agent.pid
[ -r "$PIDFILE" ] || exit 0
read -r pid < "$PIDFILE"
case "$pid" in ''|*[!0-9]*) exit 1 ;; esac
exe=$(readlink "/proc/$pid/exe" 2>/dev/null)
case "$exe" in
    "$BASE/dreame-local-agent"|"$BASE/dreame-local-agent (deleted)")
        kill "$pid" 2>/dev/null || :
        ;;
    *) exit 1 ;;
esac
for attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
    kill -0 "$pid" 2>/dev/null || { rm -f "$PIDFILE"; exit 0; }
    sleep 1
done
exit 1
