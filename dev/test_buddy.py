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
        environment = mock.patch.dict(os.environ, {"BUDDY_INDEPENDENT": ""})
        environment.start()
        self.addCleanup(environment.stop)
        buddy.BINARY.write_text("mock executable")
        buddy.BINARY.chmod(0o700)
        buddy.CONFIG.write_text("{}")

        self.alive = True
        self.process_overrides = {}
        self.patch(buddy, "process_info", side_effect=self.process_info)
        self.real_docker_access = buddy.docker_access
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

    def virtual_clock(self):
        clock = types.SimpleNamespace(now=100.0, sleeps=[])

        def sleep(duration):
            self.assertGreaterEqual(duration, 0)
            clock.sleeps.append(duration)
            clock.now += duration

        self.patch(buddy.time, "monotonic", side_effect=lambda: clock.now)
        self.patch(buddy.time, "sleep", side_effect=sleep)
        return clock

    def test_deadline_is_keyword_only_and_invalid_values_cannot_read_spawn_or_signal(self):
        clock = self.virtual_clock()
        for action in (buddy.start, buddy.stop):
            with self.subTest(action=action.__name__):
                with self.assertRaises(TypeError):
                    action(clock.now + 1)
                with mock.patch.object(buddy, "load_record") as load:
                    for value in (True, False, "101", object(), float("nan"), float("inf"), float("-inf"), 10**1000, -1, clock.now, clock.now - .1):
                        with self.subTest(deadline=value):
                            with self.assertRaisesRegex(buddy.BuddyError, "finite future same-host monotonic"):
                                action(deadline=value)
                    load.assert_not_called()
        self.spawn.assert_not_called()
        self.pidfd.assert_not_called()
        self.send_signal.assert_not_called()

    def test_future_deadline_cannot_extend_default_start_or_stop_timeout(self):
        clock = self.virtual_clock()
        self.assertEqual(buddy.start(deadline=clock.now + 1000), 0)
        self.assertEqual(self.docker.call_args.args, (100 + buddy.START_TIMEOUT,))
        start = clock.now
        self.send_signal.side_effect = lambda *args: setattr(clock, "now", clock.now + 2)
        with self.assertRaisesRegex(buddy.BuddyError, "no force kill"):
            buddy.stop(deadline=clock.now + 1000)
        self.assertAlmostEqual(clock.now, start + buddy.STOP_TIMEOUT)
        self.assertEqual(self.send_signal.call_count, 1)
        self.assertTrue(all(0 <= seconds <= .25 for seconds in clock.sleeps))

    def test_start_deadline_is_shared_by_preflight_tracking_and_readiness(self):
        clock = self.virtual_clock()
        deadline = clock.now + .1
        self.ready_pid = 99999
        self.docker.side_effect = lambda deadline: setattr(clock, "now", clock.now + .04)
        with mock.patch.object(buddy, "preflight_pid_file", side_effect=lambda: setattr(clock, "now", clock.now + .02)):
            with self.assertRaisesRegex(buddy.BuddyError, "deadline expired without readiness.*preserved"):
                buddy.start(deadline=deadline)
        self.assertEqual(self.docker.call_args.args, (deadline,))
        self.assertAlmostEqual(clock.now, deadline)
        self.assertLess(clock.sleeps[-1], .04)
        self.assertFalse(buddy.readiness(buddy.load_record()))
        self.assertEqual(self.spawn.call_count, 1)
        self.send_signal.assert_not_called()

    def test_expiry_during_preflight_preserves_log_and_forbids_launch(self):
        clock = self.virtual_clock()
        deadline = clock.now + .1
        buddy.LOG_FILE.write_text("preserve existing evidence")
        with mock.patch.object(buddy, "preflight_pid_file", side_effect=lambda: setattr(clock, "now", deadline)):
            with self.assertRaisesRegex(buddy.BuddyError, "deadline expired before launch; no client was launched"):
                buddy.start(deadline=deadline)
        self.assertEqual(buddy.LOG_FILE.read_text(), "preserve existing evidence")
        self.assertFalse(buddy.PID_FILE.exists())
        self.spawn.assert_not_called()
        self.send_signal.assert_not_called()

    def test_subsecond_tracking_deadline_caps_sleep_and_preserves_untracked_child(self):
        clock = self.virtual_clock()
        deadline = clock.now + .005
        with self.assertRaisesRegex(buddy.BuddyError, "tracking deadline.*preserved and left untouched"):
            buddy.start(deadline=deadline)
        self.assertAlmostEqual(clock.now, deadline)
        self.assertEqual(len(clock.sleeps), 1)
        self.assertAlmostEqual(clock.sleeps[0], .005)
        self.assertFalse(buddy.PID_FILE.exists())
        self.assertEqual(self.spawn.call_count, 1)
        self.send_signal.assert_not_called()

    def test_existing_ready_process_is_preserved_under_short_start_deadline(self):
        clock = self.virtual_clock()
        self.launch()
        original_record, original_log = buddy.PID_FILE.read_bytes(), buddy.LOG_FILE.read_bytes()
        self.docker.reset_mock()
        self.assertEqual(buddy.start(deadline=clock.now + .001), 0)
        with self.assertRaisesRegex(buddy.BuddyError, "finite future same-host monotonic"):
            buddy.start(deadline=clock.now)
        self.assertEqual(self.spawn.call_count, 1)
        self.docker.assert_not_called()
        self.assertEqual(buddy.PID_FILE.read_bytes(), original_record)
        self.assertEqual(buddy.LOG_FILE.read_bytes(), original_log)
        self.send_signal.assert_not_called()

    def test_deadline_does_not_weaken_exact_identity_for_start_or_stop(self):
        clock = self.virtual_clock()
        self.launch()
        self.process_overrides = {"start_ticks": 999}
        for action in (buddy.start, buddy.stop):
            with self.assertRaisesRegex(buddy.BuddyError, "no signal sent"):
                action(deadline=clock.now + 1)
        self.assertEqual(self.spawn.call_count, 1)
        self.pidfd.assert_not_called()
        self.send_signal.assert_not_called()

    def test_expiry_after_pidfd_open_closes_descriptor_without_sigterm(self):
        clock = self.virtual_clock()
        self.launch()
        deadline = clock.now + .1
        descriptors = []

        def open_pidfd(pid):
            descriptor = os.open(os.devnull, os.O_RDONLY)
            descriptors.append(descriptor)
            clock.now = deadline
            return descriptor

        self.pidfd.side_effect = open_pidfd
        with self.assertRaisesRegex(buddy.BuddyError, "deadline expired before SIGTERM; no signal sent"):
            buddy.stop(deadline=deadline)
        self.assertEqual(len(descriptors), 1)
        with self.assertRaises(OSError):
            os.fstat(descriptors[0])
        self.send_signal.assert_not_called()
        self.assertTrue(buddy.running_owned(buddy.load_record()))

    def test_stop_deadline_includes_identity_and_signal_time_without_reset(self):
        clock = self.virtual_clock()
        record = self.launch()
        deadline = clock.now + .1

        def open_pidfd(pid):
            clock.now += .03
            return os.open(os.devnull, os.O_RDONLY)

        self.pidfd.side_effect = open_pidfd
        self.send_signal.side_effect = lambda *args: setattr(clock, "now", clock.now + .02)
        with self.assertRaisesRegex(buddy.BuddyError, "deadline expired.*no force kill"):
            buddy.stop(deadline=deadline)
        self.assertAlmostEqual(clock.now, deadline)
        self.assertLess(clock.sleeps[-1], .051)
        self.send_signal.assert_called_once()
        self.assertEqual(self.send_signal.call_args.args[1], signal.SIGTERM)
        self.assertEqual(buddy.load_record()["pid"], record["pid"])

    def test_stop_cannot_report_deadline_success_from_late_exit_or_absence_observations(self):
        clock = self.virtual_clock()
        self.launch()
        deadline = clock.now + .1

        def terminate(*args):
            with buddy.LOG_FILE.open("ab") as log:
                log.write(b'level=DEBUG msg="world packet" direction=read opcode=0x04d '
                          b'opcode_label=SMSG_LOGOUT_COMPLETE bytes=0\n')
            self.alive = False

        original_scan = buddy.scan_log

        def slow_scan(*args):
            result = original_scan(*args)
            clock.now = deadline
            return result

        self.send_signal.side_effect = terminate
        with mock.patch.object(buddy, "scan_log", side_effect=slow_scan):
            with self.assertRaisesRegex(buddy.BuddyError, "exit were verified, but the stop deadline was not met; no force kill"):
                buddy.stop(deadline=deadline)
        self.send_signal.assert_called_once()
        self.assertNotIn("Stopped: PID", self.output.getvalue())

        deadline = clock.now + .1

        def slow_load():
            clock.now = deadline
            return None

        with mock.patch.object(buddy, "load_record", side_effect=slow_load):
            with self.assertRaisesRegex(buddy.BuddyError, "deadline expired during lifecycle checks; no signal sent"):
                buddy.stop(deadline=deadline)
        self.send_signal.assert_called_once()
        self.assertEqual(self.spawn.call_count, 1)

    def test_docker_waits_shrink_to_remaining_budget_without_floor_or_late_dispatch(self):
        clock = self.virtual_clock()
        deadline = clock.now + .05

        def docker_check(*args, **kwargs):
            clock.now += min(.04, kwargs["timeout"])
            return types.SimpleNamespace(returncode=0)

        with mock.patch.object(buddy.shutil, "which", return_value="/mock/docker"), \
                mock.patch.object(buddy.subprocess, "run", side_effect=docker_check) as run:
            self.real_docker_access(deadline)
            self.assertEqual(run.call_count, 2)
            self.assertAlmostEqual(run.call_args_list[0].kwargs["timeout"], .05)
            self.assertAlmostEqual(run.call_args_list[1].kwargs["timeout"], .01)
            run.reset_mock()
            clock.now = deadline - .01
            with self.assertRaisesRegex(buddy.BuddyError, "Docker access deadline expired"):
                self.real_docker_access(deadline)
            self.assertEqual(run.call_count, 1)
        self.spawn.assert_not_called()
        self.send_signal.assert_not_called()

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
        self.assertEqual(kwargs["env"]["BUDDY_INDEPENDENT"], "false")
        self.assertIs(record["independent"], False)

        self.assertEqual(buddy.start(), 0)
        self.assertEqual(self.spawn.call_count, 1)
        self.assertEqual(buddy.status(), 0)
        self.assertNotIn("PRIVATE_NEVER_PRINT", self.output.getvalue())

    def test_new_launch_records_exact_mode_and_canonical_child_environment(self):
        for requested, independent in ((None, False), ("", False), ("false", False), ("true", True)):
            with self.subTest(requested=requested), mock.patch.dict(os.environ):
                if requested is None:
                    os.environ.pop("BUDDY_INDEPENDENT", None)
                else:
                    os.environ["BUDDY_INDEPENDENT"] = requested
                # Every subcase is a new simulated launch, without a real process.
                with mock.patch.object(buddy, "load_record", return_value=None):
                    self.assertEqual(buddy.start(), 0)
                record = buddy.load_record()
                self.assertIs(record["independent"], independent)
                self.assertEqual(self.spawn_arguments[1]["env"]["BUDDY_INDEPENDENT"], "true" if independent else "false")
                self.assertEqual(buddy.start(), 0)
        self.assertEqual(self.spawn.call_count, 4)
        self.send_signal.assert_not_called()

    def test_running_companion_mode_mismatch_cannot_restart_signal_or_rewrite(self):
        self.launch()
        original_record, original_log = buddy.PID_FILE.read_bytes(), buddy.LOG_FILE.read_bytes()
        os.environ["BUDDY_INDEPENDENT"] = "true"
        with self.assertRaisesRegex(buddy.BuddyError, "BUDDY_INDEPENDENT=false.*true was requested; no signal or restart"):
            buddy.start()
        self.assertEqual(buddy.PID_FILE.read_bytes(), original_record)
        self.assertEqual(buddy.LOG_FILE.read_bytes(), original_log)
        self.assertEqual(self.spawn.call_count, 1)
        self.pidfd.assert_not_called()
        self.send_signal.assert_not_called()

    def test_recorded_independent_mode_preserves_status_and_owned_stop_in_other_environment(self):
        os.environ["BUDDY_INDEPENDENT"] = "true"
        self.launch()
        os.environ["BUDDY_INDEPENDENT"] = "false"
        with self.assertRaisesRegex(buddy.BuddyError, "BUDDY_INDEPENDENT=true.*false was requested; no signal or restart"):
            buddy.start()
        self.pidfd.assert_not_called()
        self.send_signal.assert_not_called()
        for mode in ("", "false", "invalid"):
            with self.subTest(current_mode=mode):
                os.environ["BUDDY_INDEPENDENT"] = mode
                self.assertEqual(buddy.status(), 0)
                self.assertIs(buddy.load_record()["independent"], True)
        self.assertIn("Mode: independent", self.output.getvalue())

        def terminate(pidfd, sent_signal):
            self.assertEqual(sent_signal, signal.SIGTERM)
            with buddy.LOG_FILE.open("ab") as log:
                log.write(b'level=DEBUG msg="world packet" direction=read opcode=0x04d '
                          b'opcode_label=SMSG_LOGOUT_COMPLETE bytes=0\n')
            self.alive = False

        self.send_signal.side_effect = terminate
        self.assertEqual(buddy.stop(), 0)
        self.send_signal.assert_called_once()
        self.assertEqual(self.spawn.call_count, 1)

    def test_invalid_requested_mode_fails_before_launch_or_already_running(self):
        for active in (False, True):
            if active:
                self.launch()
            before = self.spawn.call_count
            for mode in ("TRUE", "False", "1", " true", "false ", "independent"):
                with self.subTest(active=active, mode=mode), mock.patch.dict(os.environ, {"BUDDY_INDEPENDENT": mode}):
                    with self.assertRaisesRegex(buddy.BuddyError, "BUDDY_INDEPENDENT must be true or false"):
                        buddy.start()
            self.assertEqual(self.spawn.call_count, before)
        self.pidfd.assert_not_called()
        self.send_signal.assert_not_called()

    def test_legacy_records_default_to_companion_without_losing_process_ownership(self):
        original = self.launch()
        for profile_metadata in (True, False):
            with self.subTest(profile_metadata=profile_metadata):
                legacy = dict(original)
                legacy.pop("independent")
                if not profile_metadata:
                    legacy.pop("profile_identity")
                buddy.save_record(legacy)
                self.assertEqual(buddy.start(), 0)
                with mock.patch.dict(os.environ, {"BUDDY_INDEPENDENT": "true"}):
                    with self.assertRaisesRegex(buddy.BuddyError, "BUDDY_INDEPENDENT=false.*true was requested"):
                        buddy.start()
                    self.assertEqual(buddy.status(), 0)
                self.assertIn("Mode: companion", self.output.getvalue())
        self.assertEqual(self.spawn.call_count, 1)
        self.send_signal.assert_not_called()

    def test_invalid_recorded_mode_fails_closed_for_all_lifecycle_commands(self):
        record = self.launch()
        for invalid in (None, 0, 1, "true", "false", "", [], {}):
            with self.subTest(recorded_mode=invalid):
                buddy.save_record(dict(record, independent=invalid))
                original = buddy.PID_FILE.read_bytes()
                for action in (buddy.start, buddy.stop, buddy.status):
                    with self.assertRaisesRegex(buddy.BuddyError, "invalid operating mode; left untouched"):
                        action()
                self.assertEqual(buddy.PID_FILE.read_bytes(), original)
        self.assertEqual(self.spawn.call_count, 1)
        self.pidfd.assert_not_called()
        self.send_signal.assert_not_called()

    def test_stale_record_does_not_prevent_new_launch_in_requested_mode(self):
        self.launch()
        os.environ["BUDDY_INDEPENDENT"] = "true"
        # Only the old record's process is gone; the new simulated child is owned.
        with mock.patch.object(buddy, "running_owned", side_effect=(False, True, True)):
            record = self.launch()
        self.assertIs(record["independent"], True)
        self.assertEqual(self.spawn_arguments[1]["env"]["BUDDY_INDEPENDENT"], "true")
        self.assertEqual(self.spawn.call_count, 2)
        self.send_signal.assert_not_called()

    def test_exec_proc_race_waits_without_tracking_transient_argv_or_exe(self):
        exact = self.process_info(12345)
        transient = dict(exact, argv=["python3", "dev/buddy", "start"])
        mixed = dict(exact, exe_inode=999)
        samples = iter([None, transient, mixed, exact, exact])
        recorded = []
        original_save = buddy.save_record
        def save(record):
            recorded.append(dict(record))
            original_save(record)
        with mock.patch.object(buddy, "process_info", side_effect=lambda pid: next(samples, exact)), \
                mock.patch.object(buddy, "save_record", side_effect=save):
            record = self.launch()
        self.assertEqual(record["exe_inode"], exact["exe_inode"])
        self.assertTrue(recorded)
        self.assertTrue(all(row["argv"] == buddy.command_line() and row["exe_inode"] == exact["exe_inode"] for row in recorded))
        self.assertEqual(self.spawn.call_count, 1)
        self.send_signal.assert_not_called()

    def test_unsettled_child_is_preserved_without_pid_record_or_signal(self):
        self.process_overrides = {"argv": ["/usr/bin/unknown"]}
        with mock.patch.object(buddy, "EXEC_SETTLE_TIMEOUT", 0.02):
            with self.assertRaisesRegex(buddy.BuddyError, "preserved and left untouched"):
                buddy.start()
        self.assertFalse(buddy.PID_FILE.exists())
        self.assertEqual(self.spawn.call_count, 1)
        self.send_signal.assert_not_called()

    def test_child_exit_and_pid_identity_change_cannot_be_adopted(self):
        exact = self.process_info(12345)
        child = types.SimpleNamespace(pid=12345, poll=mock.Mock(side_effect=[None, 0]))
        with mock.patch.object(buddy, "process_info", return_value=dict(exact, argv=[])):
            with self.assertRaisesRegex(buddy.BuddyError, "exited before"):
                buddy.child_identity(child, buddy.time.monotonic() + 1)
        child = types.SimpleNamespace(pid=12345, poll=lambda: None)
        with mock.patch.object(buddy, "process_info", side_effect=[dict(exact, argv=[]), dict(exact, start_ticks=124)]):
            with self.assertRaisesRegex(buddy.BuddyError, "changed process identity"):
                buddy.child_identity(child, buddy.time.monotonic() + 1)
        self.assertFalse(buddy.PID_FILE.exists())
        self.send_signal.assert_not_called()

    def test_wrong_pid_never_proves_readiness_or_gets_killed(self):
        self.virtual_clock()
        self.ready_pid = 99999
        with mock.patch.object(buddy, "START_TIMEOUT", .03):
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
        clock = self.virtual_clock()
        record = self.launch()
        self.send_signal.side_effect = lambda *args: setattr(clock, "now", clock.now + buddy.STOP_TIMEOUT)
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
