"""Deploy the native ARM64 agent once over ADB; runtime requires no Pi bridge."""
import argparse
import json
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.request
import urllib.error

BASE = '/data/dreame-local'
HOOK = '/data/test/ava.sh'
MARKER = '/data/dreame-local/start.sh >/dev/null 2>&1 || :'
GUARD_MARKER = 'if [ -r /data/dreame-local/guard-env.sh ]; then . /data/dreame-local/guard-env.sh; fi'


def prepare_hook(source):
    source = source.replace('\r\n', '\n')
    first, sep, rest = source.partition('\n')
    if not sep or not first.startswith('#!') or 'sh' not in first:
        raise RuntimeError('Existing boot hook is not a recognized shell script; left unchanged')
    lines = rest.splitlines()
    lines = [line for line in lines if line not in (MARKER, GUARD_MARKER)]
    return first + '\n' + MARKER + '\n' + GUARD_MARKER + '\n' + '\n'.join(lines) + '\n'


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--serial', default='192.168.2.11:4567')
    p.add_argument('--config', type=Path, default=Path('bridge/config.json'))
    p.add_argument('--no-boot-hook', action='store_true')
    args = p.parse_args()
    root = Path(__file__).resolve().parents[1]
    source = json.loads(args.config.read_text())
    required = ('token', 'expected_model', 'expected_did', 'expected_mac')
    if not all(source.get(k) for k in required) or len(source['token']) < 32:
        p.error('Use the existing pinned bridge config with a random API token')
    config = {k: source[k] for k in required}
    config.update(bind='0.0.0.0', port=8765, allow_control=source.get('allow_control', False))
    binary = root/'agent/dreame-local-agent-linux-arm64'
    if not (root/'agent/region-guard.so').is_file():
        p.error('Packaged regional guard library is missing')
    if not binary.is_file():
        p.error('Packaged ARM64 agent binary is missing')

    def adb(*words, timeout=35):
        return subprocess.check_output(['adb', '-s', args.serial, *words], stderr=subprocess.PIPE, timeout=timeout)

    def shell(command):
        return adb('shell', command).decode().replace('\r\n', '\n')

    def push(local, remote):
        adb('push', str(local), remote)

    if shell('id -u').strip() != '0':
        p.error('Robot ADB shell is not root')
    if shell('uname -m').strip() != 'aarch64':
        p.error('This binary requires aarch64 userspace')
    # Confirm the current robot before writing any files.
    identity = shell("sed -n '/^model=/p;/^did=/p;/^mac=/p' /data/config/dmio/device.conf")
    actual = dict(line.split('=', 1) for line in identity.splitlines() if '=' in line)
    for key in ('model', 'did', 'mac'):
        if actual.get(key, '').lower() != str(config['expected_'+key]).lower():
            p.error('Robot identity mismatch: '+key)

    original_hook = None
    if not args.no_boot_hook:
        if shell('if [ -f /data/test/ava.sh ] && [ ! -L /data/test/ava.sh ]; then echo OK; fi').strip() != 'OK':
            p.error('Existing regular /data/test/ava.sh boot hook not found; use --no-boot-hook for manual start')
        original_hook = shell('cat '+HOOK)
        updated_hook = prepare_hook(original_hook)

    shell('umask 077; mkdir -p '+BASE+'; chmod 700 '+BASE)
    with tempfile.TemporaryDirectory(prefix='dreame-agent-') as tmp:
        tmp = Path(tmp)
        conf = tmp/'config.json'
        conf.write_text(json.dumps(config, indent=2)+'\n')
        os.chmod(conf, 0o600)
        push(binary, BASE+'/agent.new')
        remote_hash = shell('sha256sum '+BASE+'/agent.new').split()[0]
        if remote_hash != hashlib.sha256(binary.read_bytes()).hexdigest():
            raise RuntimeError('Agent checksum mismatch after upload')
        shell('chmod 700 '+BASE+'/agent.new')
        print(shell(BASE+'/agent.new --check-guard').strip())
        push(conf, BASE+'/config.new')
        for name in ('region-guard.so', 'guard-env.sh'):
            local = root/'agent'/name
            push(local, BASE+'/'+name+'.new')
            if shell('sha256sum '+BASE+'/'+name+'.new').split()[0] != hashlib.sha256(local.read_bytes()).hexdigest():
                raise RuntimeError('Checksum mismatch: '+name)
        for name in ('start.sh', 'stop.sh'):
            push(root/'agent'/name, BASE+'/'+name+'.new')
        shell('sh -n '+BASE+'/start.sh.new && sh -n '+BASE+'/stop.sh.new && sh -n '+BASE+'/guard-env.sh.new')
        shell('if [ -x '+BASE+'/stop.sh ]; then '+BASE+'/stop.sh; fi')
        shell('chmod 700 '+BASE+'/agent.new '+BASE+'/start.sh.new '+BASE+'/stop.sh.new; chmod 600 '+BASE+'/config.new; '
              'mv '+BASE+'/agent.new '+BASE+'/dreame-local-agent && mv '+BASE+'/config.new '+BASE+'/config.json && '
              'mv '+BASE+'/start.sh.new '+BASE+'/start.sh && mv '+BASE+'/stop.sh.new '+BASE+'/stop.sh && '
              'chmod 600 '+BASE+'/region-guard.so.new '+BASE+'/guard-env.sh.new && '
              'mv '+BASE+'/region-guard.so.new '+BASE+'/region-guard.so && mv '+BASE+'/guard-env.sh.new '+BASE+'/guard-env.sh')
        print(shell(BASE+'/dreame-local-agent --version').strip())
        shell(BASE+'/start.sh')
        # The host reaches the robot directly; no local bridge or proxy is used.
        host = args.serial.rsplit(':', 1)[0]
        url = 'http://'+host+':8765'
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        last_error = None
        for attempt in range(5):
            try:
                req = urllib.request.Request(url+'/rpc', data=b'{"method":"miIO.info"}',
                    headers={'Authorization':'Bearer '+config['token'], 'Content-Type':'application/json'})
                with opener.open(req, timeout=5) as r:
                    info = json.load(r)['result']
                if str(info.get('did')) != str(config['expected_did']):
                    raise RuntimeError('Native API returned another robot identity')
                last_error = None
                break
            except (OSError, ValueError, KeyError) as exc:
                last_error = str(exc.reason) if isinstance(exc, urllib.error.URLError) else type(exc).__name__
                time.sleep(1)
        if last_error:
            raise RuntimeError('Native API check failed: '+last_error+'; inspect /tmp/dreame-local-agent.log')
        print('Native API verified:', url)
        if original_hook is not None and updated_hook != original_hook:
            hook = tmp/'ava.sh'
            hook.write_text(updated_hook)
            os.chmod(hook, 0o700)
            push(hook, '/data/test/ava.sh.agent-new')
            shell('sh -n /data/test/ava.sh.agent-new')
            # Keep the original root hook and all its ADB bootstrap commands.
            if shell('cat '+HOOK) != original_hook:
                raise RuntimeError('Boot hook changed during installation; refusing to replace it')
            shell('if [ ! -e /data/test/ava.sh.before-agent ]; then cp -p '+HOOK+' /data/test/ava.sh.before-agent; fi; '
                  'chmod 700 /data/test/ava.sh.agent-new && mv /data/test/ava.sh.agent-new '+HOOK)
            print('Agent startup added to existing boot hook; backup: /data/test/ava.sh.before-agent')
        elif original_hook is not None:
            print('Agent startup already configured')
    print('Regional guard library will load in firmware after the next robot reboot; mode is off by default.')
    print('Update the integration URL to',url,'using its Configure button; keep the same token.')
    print('After Home Assistant connects, stop the old Pi bridge: systemctl --user disable --now dreame-local.service')


if __name__ == '__main__':
    main()
