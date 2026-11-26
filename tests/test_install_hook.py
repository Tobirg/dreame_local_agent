"""Boot hook rewrite of tools/install_agent.py: no robot or ADB access."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('install_agent', Path(__file__).resolve().parents[1] / 'tools' / 'install_agent.py')
install = importlib.util.module_from_spec(spec)
spec.loader.exec_module(install)


class HookTests(unittest.TestCase):
    def test_legacy_guard_line_becomes_module_boot_env(self):
        old = '#!/bin/sh\r\n' + install.MARKER + '\r\n' + install.LEGACY_GUARD_MARKER + '\r\n\r\nava -f conf force &\r\n'
        new = install.prepare_hook(old)
        self.assertEqual(new.splitlines()[:3], ['#!/bin/sh', install.MARKER, install.BOOT_MARKER])
        self.assertNotIn('guard-env', new)
        self.assertIn('ava -f conf force &', new)
        self.assertEqual(install.prepare_hook(new), new)

    def test_unknown_hook_left_unchanged(self):
        with self.assertRaises(RuntimeError):
            install.prepare_hook('not a script')


class ConfigTests(unittest.TestCase):
    def test_identity_and_new_config(self):
        ident = install.parse_identity('model=dreame.vacuum.r2505\ndid=123456789\nmac=AA:BB:CC:DD:EE:FF\n')
        self.assertEqual(ident, {'model': 'dreame.vacuum.r2505', 'did': '123456789', 'mac': 'aa:bb:cc:dd:ee:ff'})
        a, b = install.new_config(ident, True), install.new_config(ident, False)
        self.assertGreaterEqual(len(a['token']), 32)
        self.assertNotEqual(a['token'], b['token'])
        self.assertEqual((a['expected_did'], a['allow_control'], b['allow_control']), ('123456789', True, False))
        with self.assertRaises(RuntimeError):
            install.parse_identity('model=x\n')

    def test_reinstall_keeps_tokens(self):
        installed = '{"token": "old", "tokens": [{"name": "ha"}], "allow_unverified_firmware": true}'
        merged = install.merge_config(installed, {'token': 'new', 'port': 8765})
        self.assertEqual(merged, {'token': 'new', 'port': 8765, 'tokens': [{'name': 'ha'}], 'allow_unverified_firmware': True})
        self.assertEqual(install.merge_config('', {'token': 'new'}), {'token': 'new'})


if __name__ == '__main__':
    unittest.main()
