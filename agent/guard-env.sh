# Sourced by the existing firmware boot hook before its original commands.
if [ -r /data/dreame-local/region-guard.so ]; then
    case ":${LD_PRELOAD:-}:" in
        *:/data/dreame-local/region-guard.so:*) ;;
        *) LD_PRELOAD="/data/dreame-local/region-guard.so${LD_PRELOAD:+:$LD_PRELOAD}"; export LD_PRELOAD ;;
    esac
fi
