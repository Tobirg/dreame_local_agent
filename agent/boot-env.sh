# Sourced by the firmware boot hook (/data/test/ava.sh) before ava starts.
# Each installed module may ship boot-env.sh, e.g. to preload a library into ava; it must not exit.
for module_env in /data/dreame-local/modules/*/boot-env.sh; do
    if [ -r "$module_env" ]; then . "$module_env"; fi
done
unset module_env
