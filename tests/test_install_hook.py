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


if __name__ == '__main__':
    unittest.main()
