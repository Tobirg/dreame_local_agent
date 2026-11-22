"""Deploy the native ARM64 agent once over ADB; runtime requires no Pi bridge."""
import argparse
import json
import hashlib
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.request
import urllib.error

BASE = '/data/dreame-local'
HOOK = '/data/test/ava.sh'
MARKER = '/data/dreame-local/start.sh >/dev/null 2>&1 || :'
BOOT_MARKER = 'if [ -r /data/dreame-local/boot-env.sh ]; then . /data/dreame-local/boot-env.sh; fi'
# Hook line of agents before 0.7.0, when the region guard was built in.
LEGACY_GUARD_MARKER = 'if [ -r /data/dreame-local/guard-env.sh ]; then . /data/dreame-local/guard-env.sh; fi'


def prepare_hook(source):
    source = source.replace('\r\n', '\n')
    first, sep, rest = source.partition('\n')
    if not sep or not first.startswith('#!') or 'sh' not in first:
        raise RuntimeError('Existing boot hook is not a recognized shell script; left unchanged')
    lines = rest.splitlines()
    lines = [line for line in lines if line not in (MARKER, BOOT_MARKER, LEGACY_GUARD_MARKER)]
    return first + '\n' + MARKER + '\n' + BOOT_MARKER + '\n' + '\n'.join(lines) + '\n'


def parse_identity(text):
    """model, did and mac from the robot's device.conf lines (mac lower-case, as the agent compares it)."""
    found = dict(line.strip().split('=', 1) for line in text.splitlines() if '=' in line)
    identity = {k: found.get(k, '').strip() for k in ('model', 'did', 'mac')}
    identity['mac'] = identity['mac'].lower()
    if not all(identity.values()):
        raise RuntimeError('Robot device.conf has no model, did or mac')
    return identity


def new_config(identity, allow_control):
    """Agent config pinned to this robot, with a fresh random API token."""
    return {'token': secrets.token_hex(24), 'expected_model': identity['model'], 'expected_did': identity['did'],
            'expected_mac': identity['mac'], 'allow_control': allow_control}


def merge_config(installed_text, config):
    """Keep fields of an installed config the installer does not manage (scoped tokens, opt-ins)."""
    try:
        installed = json.loads(installed_text)
    except ValueError:
        installed = {}
    return {**(installed if isinstance(installed, dict) else {}), **config}


def default_binary(root):
    """Newest release build from build.sh, or the older agent/ location."""
    builds = sorted((root/'dist').glob('dreame-local-agent-*-linux-arm64'), key=lambda f: f.stat().st_mtime)
    return builds[-1] if builds else root/'agent/dreame-local-agent-linux-arm64'


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--serial', default='192.168.2.11:4567')
    p.add_argument('--config', type=Path, default=Path('bridge/config.json'),
                   help='pinned config; created from the robot with a new token when the file does not exist')
    p.add_argument('--read-only', action='store_true', help='new config without control (status and map only)')
    p.add_argument('--binary', type=Path, help='agent build (default: newest dist/ build from build.sh)')
    p.add_argument('--no-boot-hook', action='store_true')
    args = p.parse_args()
    root = Path(__file__).resolve().parents[1]
    binary = args.binary or default_binary(root)
    if not binary.is_file():
        p.error('ARM64 agent build is missing; run: sh build.sh')

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
    actual = parse_identity(shell("sed -n '/^model=/p;/^did=/p;/^mac=/p' /data/config/dmio/device.conf"))
    if args.config.exists():
        source = json.loads(args.config.read_text())
        required = ('token', 'expected_model', 'expected_did', 'expected_mac')
        if not all(source.get(k) for k in required) or len(source['token']) < 32:
            p.error(str(args.config)+' needs a random token of 32+ characters and expected_model, expected_did, expected_mac')
        config = {k: source[k] for k in required}
        config['allow_control'] = source.get('allow_control', False)
        for key in ('model', 'did', 'mac'):
            if actual[key].lower() != str(config['expected_'+key]).lower():
                p.error('Robot identity mismatch: '+key)
    else:
        config = new_config(actual, not args.read_only)
        args.config.parent.mkdir(parents=True, exist_ok=True)
        args.config.write_text(json.dumps(config, indent=2)+'\n')
        os.chmod(args.config, 0o600)
        print('New config for', actual['model'], 'saved to', args.config, '(keep it private: it holds the API token)')
    config.update(bind='0.0.0.0', port=8765)

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
        conf.write_text(json.dumps(merge_config(shell('cat '+BASE+'/config.json 2>/dev/null || :'), config), indent=2)+'\n')
        os.chmod(conf, 0o600)
        push(binary, BASE+'/agent.new')
        remote_hash = shell('sha256sum '+BASE+'/agent.new').split()[0]
        if remote_hash != hashlib.sha256(binary.read_bytes()).hexdigest():
            raise RuntimeError('Agent checksum mismatch after upload')
        shell('chmod 700 '+BASE+'/agent.new')
        push(conf, BASE+'/config.new')
        for name in ('start.sh', 'stop.sh', 'boot-env.sh'):
            push(root/'agent'/name, BASE+'/'+name+'.new')
        shell('sh -n '+BASE+'/start.sh.new && sh -n '+BASE+'/stop.sh.new && sh -n '+BASE+'/boot-env.sh.new')
        shell('if [ -x '+BASE+'/stop.sh ]; then '+BASE+'/stop.sh; fi')
        shell('chmod 700 '+BASE+'/agent.new '+BASE+'/start.sh.new '+BASE+'/stop.sh.new; chmod 600 '+BASE+'/config.new; '
              'mv '+BASE+'/agent.new '+BASE+'/dreame-local-agent && mv '+BASE+'/config.new '+BASE+'/config.json && '
              'mv '+BASE+'/start.sh.new '+BASE+'/start.sh && mv '+BASE+'/stop.sh.new '+BASE+'/stop.sh && '
              'chmod 600 '+BASE+'/boot-env.sh.new && mv '+BASE+'/boot-env.sh.new '+BASE+'/boot-env.sh && '
              'mkdir -p '+BASE+'/modules && chmod 700 '+BASE+'/modules && rm -f '+BASE+'/guard-env.sh')
        legacy_guard = shell('if [ -e '+BASE+'/region-guard.so ]; then echo yes; fi').strip() == 'yes'
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
        try:
            req = urllib.request.Request(url+'/api/v1/capabilities', headers={'Authorization': 'Bearer '+config['token']})
            with opener.open(req, timeout=10) as r:
                caps = json.load(r)['result']
            fw = caps.get('firmware', {})
            print('Firmware', caps['identity'].get('fw_ver'), 'of', caps['identity'].get('model') + ':',
                  'checked profile '+fw['profile'] if fw.get('verified') else
                  'no checked profile, the agent stays read-only (see README: allow_unverified_firmware)')
            print('Control:', 'on' if caps.get('control') else 'off')
        except (OSError, ValueError, KeyError) as exc:
            print('Could not read capabilities:', type(exc).__name__)
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
    if legacy_guard:
        print('The built-in region guard of older agents is no longer loaded at boot. Install the region module '
              'in the web UI (Modules) before rebooting the robot to keep the protection.')
    print('Open', url+'/', 'and sign in with the token from', args.config)


if __name__ == '__main__':
    main()
