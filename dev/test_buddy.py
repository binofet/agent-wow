"""Isolated lifecycle checks for dev/buddy; no Docker, real processes, or signals."""

import contextlib
import importlib.machinery
import importlib.util
import io
import os
from pathlib import Path
import signal
import tempfile
import types
import unittest
from unittest import mock


# The executable has no .py extension. Resolve it beside this test so invocation
# works from any working directory without a machine-specific checkout path.
loader = importlib.machinery.SourceFileLoader(
    "buddy_launcher_under_test", str(Path(__file__).resolve().with_name("buddy"))
)
spec = importlib.util.spec_from_loader(loader.name, loader)
buddy = importlib.util.module_from_spec(spec)
loader.exec_module(buddy)


class BuddyLifecycleTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        for directory in ("config", "bin", "modules"):
            (root / directory).mkdir()
        paths = {
            "ROOT": root,
            "BINARY": root / "bin" / "agent-wow",
            "CONFIG": root / "config" / "buddy.yaml",
            "PID_FILE": root / "config" / "buddy.pid",
            "LOG_FILE": root / "config" / "buddy-live.log",
            "LOCK_FILE": root / "config" / "buddy.lock",
        }
        for name, value in paths.items():
            self.patch(buddy, name, value)
        for name,value in {"PROFILE":"default","CHARACTER":"Arlen","CHARACTER_GUID":None,"RPC_PORT":8086,"CONFIG_DIR":None,"DATA_DIR":None,"MODULE_DIR":None}.items():
            self.patch(buddy,name,value)
        buddy.BINARY.write_text("mock executable")
        buddy.BINARY.chmod(0o700)
        buddy.CONFIG.write_text("{}")

        self.alive = True
        self.process_overrides = {}
        self.patch(buddy, "process_info", side_effect=self.process_info)
        self.docker = self.patch(buddy, "docker_access")
        self.socket = self.patch(buddy.socket, "socket")
        self.ready_pid = 12345
        self.spawn = self.patch(buddy.subprocess, "Popen", side_effect=self.spawn_body)

        # A harmless real descriptor exercises the launcher's close() handling.
        # No actual pidfd or process signal is created, even on Linux CI runners.
        self.pidfd = self.patch(
            buddy.os, "pidfd_open",
            side_effect=lambda pid: os.open(os.devnull, os.O_RDONLY), create=True,
        )
        self.send_signal = self.patch(buddy.signal, "pidfd_send_signal", create=True)
        self.output = io.StringIO()
        redirect = contextlib.redirect_stdout(self.output)
        redirect.__enter__()
        self.addCleanup(redirect.__exit__, None, None, None)

    def patch(self, target, name, *args, **kwargs):
        patcher = mock.patch.object(target, name, *args, **kwargs)
        result = patcher.start()
        self.addCleanup(patcher.stop)
        return result

    def process_info(self, pid):
        if not self.alive:
            return None
        return dict({
            "argv": buddy.command_line(),
            "start_ticks": 123,
            "exe_device": 1,
            "exe_inode": 2,
        }, **self.process_overrides)

    def spawn_body(self, *args, **kwargs):
        self.spawn_arguments = args, kwargs
        # This is the same slog event shape emitted by cmd/char_play.go. The
        # unrelated private line must never appear in launcher output.
        line = (
            'PRIVATE_NEVER_PRINT\nlevel=INFO msg="world session ready" event=ready '
            f'rpc_url=http://127.0.0.1:{buddy.RPC_PORT}/rpc pid={self.ready_pid} '
            f'character.name={buddy.CHARACTER} character.guid={buddy.CHARACTER_GUID or 1}\n'
        )
        os.write(kwargs["stdout"].fileno(), line.encode())
        return types.SimpleNamespace(pid=12345, poll=lambda: None)

    def launch(self):
        self.assertEqual(buddy.start(), 0)
        return buddy.load_record()

    def test_private_detached_launch_and_exact_readiness(self):
        record = self.launch()
        self.assertEqual(record["pid"], 12345)
        self.assertIsNotNone(record["ready_offset"])
        for path in (buddy.PID_FILE, buddy.LOG_FILE):
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        args, kwargs = self.spawn_arguments
        self.assertEqual(args[0], buddy.command_line())
        self.assertTrue(kwargs["start_new_session"])
        self.assertTrue(kwargs["close_fds"])
        self.assertEqual(kwargs["env"]["AGENT_WOW_MODULE_DIR"], str(buddy.ROOT / "modules"))
        self.assertEqual(kwargs["env"]["AGENT_WOW_LOG_LEVEL"], "debug")

        self.assertEqual(buddy.start(), 0)
        self.assertEqual(self.spawn.call_count, 1)
        self.assertEqual(buddy.status(), 0)
        self.assertNotIn("PRIVATE_NEVER_PRINT", self.output.getvalue())

    def test_wrong_pid_never_proves_readiness_or_gets_killed(self):
        self.ready_pid = 99999
        with mock.patch.object(buddy, "START_TIMEOUT", 0.01):
            with self.assertRaisesRegex(buddy.BuddyError, "preserved"):
                buddy.start()
        self.assertFalse(buddy.readiness(buddy.load_record()))
        self.send_signal.assert_not_called()

    def test_unknown_or_reused_process_is_not_signaled_or_restarted(self):
        self.launch()
        for field, replacement in (
            ("argv", ["/usr/bin/unknown"]),
            ("start_ticks", 999),
            ("exe_device", 999),
            ("exe_inode", 999),
        ):
            self.process_overrides = {field: replacement}
            for action in (buddy.start, buddy.stop, buddy.status):
                with self.subTest(field=field, action=action.__name__):
                    with self.assertRaisesRegex(buddy.BuddyError, "no signal sent"):
                        action()
        self.pidfd.assert_not_called()
        self.send_signal.assert_not_called()
        self.assertEqual(self.spawn.call_count, 1)

    def test_occupied_port_preserves_previous_log_and_processes(self):
        self.launch()
        original = buddy.LOG_FILE.read_bytes()
        self.alive = False
        self.socket.return_value.__enter__.return_value.bind.side_effect = OSError("occupied")
        with self.assertRaisesRegex(buddy.BuddyError, "occupied"):
            buddy.start()
        self.assertEqual(buddy.LOG_FILE.read_bytes(), original)
        self.assertEqual(self.spawn.call_count, 1)
        self.send_signal.assert_not_called()

    def test_stop_requires_server_completion_and_exit(self):
        self.launch()

        def terminate(pidfd, sent_signal):
            self.assertEqual(sent_signal, signal.SIGTERM)
            with buddy.LOG_FILE.open("ab") as log:
                log.write(
                    b'level=DEBUG msg="world packet" direction=read opcode=0x04d '
                    b'opcode_label=SMSG_LOGOUT_COMPLETE bytes=0\n'
                )
            self.alive = False

        self.send_signal.side_effect = terminate
        self.assertEqual(buddy.stop(), 0)
        self.send_signal.assert_called_once()
        self.assertIn("server logout completion and process exit verified", self.output.getvalue())

    def test_exit_without_confirmation_is_not_reported_confirmed(self):
        self.launch()
        self.send_signal.side_effect = lambda pidfd, sent_signal: setattr(self, "alive", False)
        with self.assertRaisesRegex(buddy.BuddyError, "logout is unverified"):
            buddy.stop()
        self.send_signal.assert_called_once()

    def test_stop_timeout_never_escalates_to_kill(self):
        record = self.launch()
        with mock.patch.object(buddy, "STOP_TIMEOUT", 0):
            with self.assertRaisesRegex(buddy.BuddyError, "no force kill"):
                buddy.stop()
        self.send_signal.assert_called_once()
        self.assertEqual(self.send_signal.call_args.args[1], signal.SIGTERM)
        self.assertEqual(buddy.load_record()["pid"], record["pid"])

    def test_legacy_pid_file_is_preserved(self):
        buddy.PID_FILE.write_text("909640\n")
        original = buddy.PID_FILE.read_bytes()
        with self.assertRaisesRegex(buddy.BuddyError, "left untouched"):
            buddy.start()
        self.assertEqual(buddy.PID_FILE.read_bytes(), original)
        self.spawn.assert_not_called()
        self.send_signal.assert_not_called()

    def test_replaced_log_cannot_prove_readiness(self):
        record = self.launch()
        replacement = buddy.LOG_FILE.with_name("new.log")
        replacement.write_bytes(buddy.LOG_FILE.read_bytes())
        replacement.replace(buddy.LOG_FILE)
        with self.assertRaisesRegex(buddy.BuddyError, "replaced"):
            buddy.readiness(record)

    def test_ready_event_needs_exact_pid_character_and_url(self):
        line = (
            b'level=INFO msg="world session ready" event=ready pid=12345 '
            b'character.name=Arlen rpc_url=http://127.0.0.1:8086/rpc\n'
        )
        self.assertTrue(buddy.ready_line(line, 12345))
        self.assertFalse(buddy.ready_line(line, 999))
        self.assertFalse(buddy.ready_line(line.replace(b"Arlen", b"Unknown"), 12345))
        self.assertFalse(buddy.ready_line(line.replace(b"8086", b"8087"), 12345))

    def test_preflight_failure_prevents_spawn(self):
        with mock.patch.object(buddy, "preflight_pid_file", side_effect=OSError("disk full")):
            with self.assertRaises(OSError):
                buddy.start()
        self.spawn.assert_not_called()
        self.assertFalse(buddy.LOG_FILE.exists())

    def test_initial_persistence_failure_names_preserved_child(self):
        with mock.patch.object(buddy.os, "replace", side_effect=OSError("disk full")):
            with self.assertRaisesRegex(buddy.BuddyError, "PID 12345 was preserved"):
                buddy.start()
        self.assertEqual(self.spawn.call_count, 1)
        self.send_signal.assert_not_called()
        self.assertFalse(buddy.PID_FILE.exists())

    def test_failed_atomic_update_preserves_valid_record(self):
        record = self.launch()
        original = buddy.PID_FILE.read_bytes()
        record["ready_offset"] = None
        with mock.patch.object(buddy.os, "replace", side_effect=OSError("disk full")):
            with self.assertRaisesRegex(buddy.BuddyError, "PID 12345 was preserved"):
                buddy.save_record(record)
        self.assertEqual(buddy.PID_FILE.read_bytes(), original)
        self.assertIsNotNone(buddy.load_record()["ready_offset"])
        self.assertFalse(list(buddy.PID_FILE.parent.glob(".buddy-pid-*")))

    def profile(self, **overrides):
        args = dict(profile="mage",character_name="Mira",character_guid=10,rpc_port=8087,
                    config=None,config_dir=None,data_dir=None,module_dir=None)
        args.update(overrides)
        buddy.configure(types.SimpleNamespace(**args))
        buddy.PID_FILE.parent.mkdir(parents=True,mode=0o700,exist_ok=True)
        buddy.CONFIG.write_text("{}")

    def test_named_profile_separates_auth_data_pid_log_and_ready_identity(self):
        original_config = buddy.CONFIG
        self.profile()
        record=self.launch()
        self.assertEqual(buddy.CONFIG,buddy.ROOT/"config/buddies/mage/buddy.yaml")
        self.assertNotEqual(original_config,buddy.CONFIG)
        self.assertEqual(record["profile_identity"]["character_guid"],10)
        environment=self.spawn_arguments[1]["env"]
        self.assertEqual(environment["AGENT_WOW_CONFIG_DIR"],str(buddy.CONFIG_DIR))
        self.assertEqual(environment["AGENT_WOW_AUTH_FILE_PATH"],str(buddy.CONFIG_DIR/"auth.json"))
        self.assertEqual(environment["AGENT_WOW_DATA_DIR"],str(buddy.ROOT/"data/buddies/mage"))
        self.assertEqual(environment["AGENT_WOW_WORLDRPC_PORT"],"8087")
        self.assertEqual(record["argv"],buddy.command_line())
        self.assertFalse(buddy.ready_line(b'level=INFO msg="world session ready" event=ready pid=12345 character.name=Mira character.guid=11 rpc_url=http://127.0.0.1:8087/rpc\n',12345))

    def test_other_profile_record_is_never_signaled_even_with_matching_pid(self):
        self.profile()
        self.launch()
        record=buddy.PID_FILE.read_bytes()
        self.profile(profile="rogue",character_name="Rook",character_guid=11,rpc_port=8088)
        buddy.PID_FILE.write_bytes(record)
        for action in (buddy.start,buddy.stop,buddy.status):
            with self.assertRaisesRegex(buddy.BuddyError,"left untouched"):
                action()
        self.send_signal.assert_not_called()

    def test_profile_arguments_bound_identity_and_preserve_default(self):
        base=dict(profile="default",character_name=None,character_guid=None,rpc_port=None,config=None,config_dir=None,data_dir=None,module_dir=None)
        buddy.configure(types.SimpleNamespace(**base))
        self.assertEqual(buddy.CONFIG,buddy.ROOT/"config/buddy.yaml")
        self.assertIsNone(buddy.CONFIG_DIR)
        for change in ({"profile":"../other"},{"profile":"mage"},{"character_name":"Mira/"},{"character_guid":3<<48},{"rpc_port":80}):
            with self.assertRaises(buddy.BuddyError):
                buddy.configure(types.SimpleNamespace(**dict(base,**change)))


if __name__ == "__main__":
    unittest.main(verbosity=2)
