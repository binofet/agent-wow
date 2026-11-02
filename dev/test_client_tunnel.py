import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest

loader = importlib.machinery.SourceFileLoader('client_tunnel_tested', str(Path(__file__).with_name('client-tunnel')))
spec = importlib.util.spec_from_loader(loader.name, loader)
tunnel = importlib.util.module_from_spec(spec)
loader.exec_module(tunnel)


class ClientTunnelTests(unittest.TestCase):
    def test_one_owner_per_group_and_independent_nix_group(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            first = tunnel.reserve(folder, 'party')
            try:
                with self.assertRaises(BlockingIOError): tunnel.reserve(folder, 'party')
                other = tunnel.reserve(folder, 'nix')
                other.close()
            finally: first.close()
            tunnel.reserve(folder, 'party').close()

    def test_signal_identity_requires_exact_process_start_and_argv(self):
        signature = 'Sun Nov 1 17:10:00 2026 /trusted/python -B /trusted/dev/client-tunnel run --group nix'
        record = dict(pid=321, process=signature)
        self.assertTrue(tunnel.owns(record, signature))
        for current in ('', signature.replace('17:10', '17:11'), signature.replace('--group nix', '--group party')):
            self.assertFalse(tunnel.owns(record, current))
        self.assertFalse(tunnel.owns(dict(pid=True, process=signature), signature))

    def test_fixed_loopback_groups_no_remote_or_avatar_commands(self):
        command = tunnel.command('nix')
        self.assertEqual(command[-1], 'quasar')
        self.assertIn('127.0.0.1:8402:127.0.0.1:8402', command)
        self.assertNotIn('-f', command)
        self.assertIn('ExitOnForwardFailure=yes', command)
        self.assertIn('ServerAliveCountMax=4', command)
        self.assertEqual(command.count('-L'), 1)
