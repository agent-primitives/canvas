import contextlib
import importlib.util
import io
import os
import pathlib
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

HOSTED = '{"profile":"hosted","hosted":{"endpoint":"https://example-resource.openai.azure.com/openai/v1","model":"example-deployment"}}'

spec = importlib.util.spec_from_file_location("canvas_dev", pathlib.Path(__file__).with_name("dev.py"))
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)


def process_running(pid):
    try:
        # Orphaned descendants may briefly remain as zombies until init reaps.
        return pathlib.Path(f"/proc/{pid}/stat").read_text().split()[2] != "Z"
    except FileNotFoundError:
        result = subprocess.run(["ps", "-o", "stat=", "-p", str(pid)], capture_output=True, text=True, check=False)
        state = result.stdout.strip()
        return result.returncode == 0 and bool(state) and not state.startswith("Z")


class DevProfileTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.config = self.root / "model.config.json"
        self.local_config = self.root / "model.config.local.json"
        self.steps = []
        self.options = []
        self.commands = []
        self.ports = []
        self.ready_ports = []
        self.steps_at_runtime = []

    def launch(self, *args):
        def finite(command, stop, **options):
            self.steps.append(command)
            self.options.append(options)
            return getattr(self, "probe_result", 0) if "-check-model" in command else 0

        def children(commands, readiness, stop):
            self.steps_at_runtime = list(self.steps)
            self.commands.extend(commands)
            for ready in readiness:
                ready()

        with patch.object(dev, "ROOT", self.root), patch.object(sys, "argv", ["dev", *args]), \
             patch.object(dev.signal, "signal"), patch.object(dev, "run_command", finite), \
             patch.object(dev, "run_children", children), \
             patch.object(dev, "check_listeners", self.ports.extend), \
             patch.object(dev, "reachable", self.ready_ports.append), \
             contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            dev.main()

    def assert_profile(self, profile):
        self.assertEqual([pathlib.Path(c[0]).name for c in self.commands], ["canvas"])
        for command in (self.steps[-1], self.commands[0]):
            self.assertEqual(command[command.index("-model-profile") + 1], profile)

    def test_missing_config_defaults_offline(self):
        self.launch()
        self.assertEqual(pathlib.Path(self.steps[0][1]).name, "ollama.py")
        self.assertEqual([pathlib.Path(c[0]).name for c in self.steps[1:]], ["go", "go", "sessions-host"])
        self.assert_profile("offline")

    def test_local_config_replaces_shipped_file(self):
        for shipped in (None, '{broken', '{"profile":"offline"}', HOSTED):
            with self.subTest(shipped=shipped):
                if shipped is not None:
                    self.config.write_text(shipped)
                self.local_config.write_text(HOSTED)
                self.steps.clear()
                self.commands.clear()
                try:
                    self.launch()
                except RuntimeError as error:
                    self.fail(f"valid local config must replace shipped config: {error}")
                self.assertEqual([pathlib.Path(c[0]).name for c in self.steps], ["go", "go", "sessions-host"])
                self.assert_profile("hosted")

    def test_local_config_does_not_merge_hosted_settings(self):
        self.config.write_text(HOSTED)
        for data in ('{"profile":"hosted"}', '{"profile":"offline"}'):
            for override in ((), ("--model-profile", "hosted")):
                if data == '{"profile":"offline"}' and not override:
                    continue
                with self.subTest(data=data, override=override):
                    self.local_config.write_text(data)
                    self.steps.clear()
                    self.commands.clear()
                    with self.assertRaises(RuntimeError) as failure:
                        self.launch(*override)
                    self.assertIn("endpoint", str(failure.exception))
                    self.assertIn("model.config.local.json", str(failure.exception))
                    self.assertEqual(self.steps, [])
        self.steps.clear()
        self.commands.clear()
        self.launch("--model-profile", "offline")
        self.assert_profile("offline")

    def test_local_offline_replaces_hosted_and_cli_override_still_wins(self):
        self.config.write_text(HOSTED)
        self.local_config.write_text('{"profile":"offline"}')
        self.launch()
        self.assertEqual(pathlib.Path(self.steps[0][1]).name, "ollama.py")
        self.assert_profile("offline")
        for configured, override in (("offline", "hosted"), ("hosted", "offline")):
            with self.subTest(configured=configured, override=override):
                self.local_config.write_text('{"profile":"' + configured + '","hosted":{"endpoint":"https://x.example/v1","model":"m"}}')
                self.steps.clear()
                self.commands.clear()
                self.launch("--model-profile", override)
                self.assertEqual(len(self.steps), 4 if override == "offline" else 3)
                self.assert_profile(override)

    def test_ollama_setup_output_is_shown(self):
        self.launch()
        self.assertEqual(self.options[0], {"show_output": True})
        self.assertEqual(self.options[1:], [{}, {}, {"show_output": True}])

    def test_hosted_config_ignores_model_port_for_probe_and_canvas(self):
        self.config.write_text(HOSTED)
        for port in ("0", "65536", "8765", "8766"):
            with self.subTest(port=port):
                self.steps.clear()
                self.commands.clear()
                self.ports.clear()
                self.launch("--model-port", port)
                self.assertEqual([pathlib.Path(c[0]).name for c in self.steps], ["go", "go", "sessions-host"], "hosted must not start or pull Ollama")
                self.assertEqual(self.ports, [8765])
                self.assert_profile("hosted")

    def test_full_hosted_schema_is_accepted(self):
        self.config.write_text('{"profile":"hosted","hosted":{"endpoint":"https://api.example.com/v1",'
                               '"model":"m","keyFile":"~/.config/canvas/other.key","auth":"bearer"}}')
        self.launch()
        self.assert_profile("hosted")

    def test_offline_config_starts_owned_ollama_for_embedded_canvas(self):
        self.config.write_text('{"profile":"offline"}')
        self.launch("--model-port", "12434")
        self.assertEqual(self.steps[0], [sys.executable, str(self.root / "scripts/ollama.py"), "start", "--port", "12434",
                                         "--container-engine", "auto"])
        self.assertEqual([pathlib.Path(c[0]).name for c in self.steps[1:]], ["go", "go", "sessions-host"])
        self.assert_profile("offline")
        canvas = self.commands[0]
        self.assertEqual(canvas[canvas.index("-model-base") + 1], "http://127.0.0.1:12434")

    def test_explicit_container_engine_is_passed_only_to_offline_setup(self):
        self.launch("--container-engine", "podman")
        self.assertEqual(self.steps[0][-2:], ["--container-engine", "podman"])
        self.steps.clear()
        self.commands.clear()
        self.config.write_text(HOSTED)
        self.launch("--container-engine", "podman")
        self.assertEqual([pathlib.Path(c[0]).name for c in self.steps], ["go", "go", "sessions-host"])

    def test_override_wins_for_probe_and_canvas(self):
        hosted = '{"endpoint":"https://x.example/v1","model":"m"}'
        for configured, override in (("offline", "hosted"), ("hosted", "offline")):
            with self.subTest(configured=configured, override=override):
                self.config.write_text('{"profile":"' + configured + '","hosted":' + hosted + '}')
                self.steps.clear()
                self.commands.clear()
                self.launch("--model-profile", override)
                self.assertEqual(len(self.steps), 4 if override == "offline" else 3)
                self.assert_profile(override)

    def test_offline_override_does_not_need_hosted_settings(self):
        self.config.write_text('{"profile":"hosted"}')
        self.launch("--model-profile", "offline")
        self.assert_profile("offline")

    def test_hosted_without_endpoint_and_model_is_a_clear_error(self):
        for data, override in (('{"profile":"hosted"}', ()), ('{"profile":"offline"}', ("--model-profile", "hosted")),
                               ('{"profile":"hosted","hosted":{"endpoint":"https://x.example/v1"}}', ())):
            with self.subTest(data=data, override=override):
                self.config.write_text(data)
                with self.assertRaisesRegex(RuntimeError, "endpoint"):
                    self.launch(*override)
                self.assertEqual(self.steps, [])
        self.config.unlink()
        with self.assertRaisesRegex(RuntimeError, "endpoint"):
            self.launch("--model-profile", "hosted")

    def test_bad_config_is_rejected_before_commands_even_with_override(self):
        fixtures = ('{', '{}', 'null', '[]', '{"profile":"unknown"}', '{"profile":null}',
                    '{"profile":1}', '{"profile":["hosted"]}',
                    '{"profile":"hosted","endpoint":"private"}',
                    '{"profile":"offline","profile":"hosted"}',
                    '{"profile":"offline","hosted":null}',
                    '{"profile":"offline","hosted":{"apiKey":"secret"}}',
                    '{"profile":"offline","hosted":{"model":1}}',
                    '{"profile":"offline","hosted":{"model":"a","model":"b"}}',
                    '{"profile":"hosted"} {}', '{"profile":"hosted"}' + ' ' * 16384)
        for config in (self.config, self.local_config):
            self.config.write_text('{"profile":"offline"}')
            for data in fixtures:
                for override in ((), ("--model-profile", "offline")):
                    with self.subTest(config=config.name, data=data[:60], override=override):
                        config.write_text(data)
                        with self.assertRaises(RuntimeError) as failure:
                            self.launch(*override)
                        self.assertIn(config.name, str(failure.exception))
                        self.assertEqual(self.steps, [])
                        self.assertEqual(self.commands, [])

    def test_config_error_names_the_problem(self):
        self.config.write_text('{"profile":"offline","hosted":{"apiKey":"secret"}}')
        with self.assertRaisesRegex(RuntimeError, "apiKey"):
            self.launch()

    def test_nonregular_config_is_rejected_before_commands(self):
        target = self.root / "target.json"
        target.write_text('{"profile":"offline"}')
        for config in (self.config, self.local_config):
            if config == self.local_config:
                self.config.write_text('{"profile":"offline"}')
            for kind in ("symlink", "dangling", "directory", "fifo"):
                with self.subTest(config=config.name, kind=kind):
                    if kind in ("symlink", "dangling"):
                        config.symlink_to(target if kind == "symlink" else self.root / "absent")
                    elif kind == "directory":
                        config.mkdir()
                    else:
                        os.mkfifo(config)
                    try:
                        for override in ((), ("--model-profile", "offline")):
                            with self.assertRaises(RuntimeError) as failure:
                                self.launch(*override)
                            self.assertIn(config.name, str(failure.exception))
                            self.assertEqual(self.steps, [])
                    finally:
                        if kind == "directory":
                            config.rmdir()
                        else:
                            config.unlink()

    def test_unreadable_config_does_not_fall_back(self):
        self.config.write_text('{"profile":"offline"}')
        self.local_config.write_text(HOSTED)
        original_open = dev.os.open

        def deny_local(path, *args, **kwargs):
            if pathlib.Path(path) == self.local_config:
                raise PermissionError("fixture access denied")
            return original_open(path, *args, **kwargs)

        with patch.object(dev.os, "open", side_effect=deny_local):
            for override in ((), ("--model-profile", "offline")):
                with self.assertRaises(RuntimeError) as failure:
                    self.launch(*override)
                self.assertIn(self.local_config.name, str(failure.exception))
        self.assertEqual(self.steps, [])

    def test_offline_rejects_invalid_or_conflicting_model_port_before_setup(self):
        for port in ("0", "65536", "8765"):
            with self.subTest(port=port), self.assertRaises(RuntimeError):
                self.launch("--model-profile", "offline", "--model-port", port)
        self.assertEqual(self.steps, [])

    def test_probe_precedes_only_northwind_canvas_with_isolated_state_and_http_readiness(self):
        # A missing definition/state/model flag or a remote Sessions child breaks
        # the embedded Northwind startup contract. Fake only subprocess boundaries.
        legacy = self.root / ".state" / "legacy-marker"
        legacy.parent.mkdir()
        legacy.write_text("preserve existing football state")
        for profile in ("hosted", "offline"):
            with self.subTest(profile=profile):
                self.config.write_text(HOSTED)
                self.steps.clear()
                self.commands.clear()
                self.ready_ports.clear()
                self.launch("--model-profile", profile, "--model-port", "12434", "--http-port", "8875")
                self.assertEqual(self.steps[-1], [
                    str(self.root / ".bin/sessions-host"), "-root", str(self.root),
                    "-state", str(self.root / ".state/northwind"), "-model-profile", profile,
                    "-model-base", "http://127.0.0.1:12434", "-check-model",
                ])
                self.assertEqual(self.commands, [[
                    str(self.root / ".bin/canvas"), "-root", str(self.root),
                    "-state", str(self.root / ".state/northwind"), "-model-profile", profile,
                    "-model-base", "http://127.0.0.1:12434",
                    "-definition", "examples/northwind-prd/canvas.yaml", "-listen", "127.0.0.1:8875",
                ]])
                self.assertEqual(self.steps_at_runtime, self.steps, "model probe must finish before runtime")
                self.assertEqual(self.ready_ports, [8875])
                self.assertEqual(list(legacy.parent.iterdir()), [legacy])
                self.assertEqual(legacy.read_text(), "preserve existing football state")

    def test_offline_model_can_use_former_sessions_port(self):
        try:
            self.launch("--model-port", "8766")
        except RuntimeError as error:
            self.fail(f"embedded Sessions must not reserve port 8766: {error}")
        self.assertEqual(self.ports, [8765])
        self.assertEqual(self.steps[0][3:5], ["--port", "8766"])
        canvas = self.commands[0]
        self.assertEqual(canvas[canvas.index("-model-base") + 1], "http://127.0.0.1:8766")

    def test_probe_failure_stops_before_any_children_with_safe_context(self):
        self.config.write_text(HOSTED)
        self.probe_result = 1
        with self.assertRaisesRegex(RuntimeError, "endpoint.*failed.*no generation"):
            self.launch()
        self.assertEqual(len(self.steps), 3)
        self.assertEqual(self.commands, [])

    def test_probe_cancellation_stops_before_any_children(self):
        self.config.write_text(HOSTED)
        self.probe_result = None
        self.launch()
        self.assertEqual(len(self.steps), 3)
        self.assertEqual(self.commands, [])

    def test_obsolete_sessions_port_is_rejected_before_commands(self):
        with self.assertRaises(SystemExit) as failure:
            self.launch("--sessions-port", "8876")
        self.assertEqual(failure.exception.code, 2)
        self.assertEqual(self.steps, [])
        self.assertEqual(self.commands, [])

    def test_unknown_override_is_rejected_before_commands(self):
        with self.assertRaises(SystemExit) as failure:
            self.launch("--model-profile", "unknown")
        self.assertEqual(failure.exception.code, 2)
        self.assertEqual(self.steps, [])

    def test_unknown_container_engine_is_rejected_before_commands(self):
        with self.assertRaises(SystemExit) as failure:
            self.launch("--container-engine", "unknown")
        self.assertEqual(failure.exception.code, 2)
        self.assertEqual(self.steps, [])


class MakeProfileTest(unittest.TestCase):
    def dry_run(self, target, *settings):
        # Drop make variables inherited from an outer `make test PYTHON=...` run.
        env = {k: v for k, v in os.environ.items() if k not in ("MAKEFLAGS", "MFLAGS", "MAKELEVEL", "PYTHON")}
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run(["make", "--no-print-directory", "-n", "-f", str(dev.ROOT / "Makefile"), target, *settings],
                                    cwd=directory, capture_output=True, text=True, check=True, env=env)
        return [shlex.split(line) for line in result.stdout.splitlines()]

    def test_default_setup_downloads_dependencies_only(self):
        self.assertEqual(self.dry_run("setup"), [["go", "mod", "download"]])

    def test_explicit_offline_setup_keeps_model_port_override(self):
        commands = self.dry_run("setup-offline", "MODEL_PORT=12434")
        self.assertEqual(commands, [["go", "mod", "download"], ["python3", "scripts/ollama.py", "start", "--port", "12434",
                                                                "--container-engine", "auto"]])

    def test_make_passes_explicit_container_engine_to_managed_model_commands(self):
        for target in ("setup-offline", "dev", "remove-ollama"):
            with self.subTest(target=target):
                command = self.dry_run(target, "CONTAINER_ENGINE=podman")[-1]
                self.assertEqual(command[command.index("--container-engine") + 1], "podman")

    def test_check_model_is_profile_aware_get_only_diagnostic(self):
        command = self.dry_run("check-model")[0]
        self.assertEqual(command[:4], ["go", "run", "./cmd/sessions-host", "-check-model"])
        self.assertNotIn("-model-profile", command)
        for profile in ("offline", "hosted"):
            command = self.dry_run("check-model", "MODEL_PROFILE=" + profile, "MODEL_PORT=12434")[0]
            self.assertEqual(command[command.index("-model-profile") + 1], profile)
            self.assertEqual(command[command.index("-model-base") + 1], "http://127.0.0.1:12434")
        self.assertEqual(self.dry_run("check-offline-model", "MODEL_PORT=12434"),
                         [["python3", "scripts/ollama.py", "verify", "--port", "12434"]])

    def test_dev_passes_only_http_and_model_ports(self):
        self.assertEqual(self.dry_run("dev", "HTTP_PORT=8875", "MODEL_PORT=12434", "SESSIONS_PORT=8876"), [[
            "python3", "scripts/dev.py", "--http-port", "8875", "--model-port", "12434",
            "--container-engine", "auto",
        ]])

    def test_dev_and_run_leave_config_default_or_pass_explicit_profile(self):
        for target in ("dev", "run"):
            default = self.dry_run(target)[0]
            self.assertNotIn("--model-profile", default)
            self.assertNotIn("-model-profile", default)
            for profile in ("hosted", "offline"):
                with self.subTest(target=target, profile=profile):
                    command = self.dry_run(target, "MODEL_PROFILE=" + profile, "MODEL_PORT=12434")[0]
                    flag = "--model-profile" if target == "dev" else "-model-profile"
                    self.assertEqual(command[command.index(flag) + 1], profile)
                    if target == "run":
                        self.assertEqual(command[command.index("-model-base") + 1], "http://127.0.0.1:12434")


class DevLifecycleTest(unittest.TestCase):
    def test_signal_handler_does_not_deadlock_on_stop_condition(self):
        fixture = f'''
import importlib.util, pathlib, signal, tempfile
spec = importlib.util.spec_from_file_location("fixture_dev", {str(pathlib.Path(__file__).with_name("dev.py"))!r})
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)
dev.check_listeners = lambda ports: None
def interrupted_setup(command, stop, **kwargs):
    condition = getattr(stop, "_cond", None)
    if condition is not None:
        condition.acquire()
    try:
        signal.raise_signal(signal.SIGTERM)
        return None
    finally:
        if condition is not None:
            condition.release()
dev.run_command = interrupted_setup
with tempfile.TemporaryDirectory() as root:
    dev.ROOT = pathlib.Path(root)
    dev.main()
'''
        try:
            result = subprocess.run([sys.executable, "-c", fixture, "--model-profile", "offline"], capture_output=True, timeout=3)
        except subprocess.TimeoutExpired:
            self.fail("Signal cancellation deadlocked on the stop condition")
        self.assertEqual(result.returncode, 0)

    def test_main_cancellation_during_setup_and_build_reaps_process_groups(self):
        # Exercise actual launcher signals, setup/build children, and a stubborn
        # descendant without running Docker, Go builds, or a model.
        for stage in ("setup", "build"):
            for sig in (signal.SIGTERM, signal.SIGINT):
                with self.subTest(stage=stage, signal=sig), tempfile.TemporaryDirectory() as directory:
                    root = pathlib.Path(directory)
                    (root / "scripts").mkdir()
                    (root / "tools").mkdir()
                    started = root / "started"
                    descendant_code = "import os,pathlib,signal,sys,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); pathlib.Path(sys.argv[1]).write_text(str(os.getpid())); time.sleep(60)"
                    stage_code = (
                        "import os,pathlib,subprocess,sys,time\n"
                        f"root = pathlib.Path({str(root)!r})\n"
                        f"child = subprocess.Popen([sys.executable, '-c', {descendant_code!r}, str(root / 'descendant')])\n"
                        "while not (root / 'descendant').exists(): time.sleep(0.01)\n"
                        "(root / 'started').write_text(str(os.getpid()))\n"
                        "print('private fixture output', flush=True)\n"
                        "time.sleep(60)\n"
                    )
                    (root / "scripts" / "ollama.py").write_text(stage_code if stage == "setup" else "raise SystemExit(0)\n")
                    go = root / "tools" / "go"
                    go.write_text(f"#!{sys.executable}\n" + stage_code)
                    go.chmod(0o700)
                    ports = []
                    with socket.socket() as first, socket.socket() as second:
                        for listener in (first, second):
                            listener.bind(("127.0.0.1", 0))
                            ports.append(listener.getsockname()[1])
                    launcher = (
                        "import importlib.util,pathlib,sys; "
                        f"spec=importlib.util.spec_from_file_location('dev', {str(pathlib.Path(dev.__file__).resolve())!r}); "
                        "dev=importlib.util.module_from_spec(spec); spec.loader.exec_module(dev); "
                        f"dev.ROOT=pathlib.Path({str(root)!r}); "
                        f"sys.argv=['dev', '--model-profile', 'offline', '--http-port', '{ports[0]}', '--model-port', '{ports[1]}']; "
                        "dev.main()"
                    )
                    environment = dict(os.environ, PATH=str(root / "tools") + os.pathsep + os.environ.get("PATH", ""))
                    process = subprocess.Popen([sys.executable, "-c", launcher], env=environment, start_new_session=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                    pids = []
                    try:
                        deadline = time.monotonic() + 5
                        def written(path):
                            return path.exists() and path.read_text() != ""
                        while not (written(started) and written(root / "descendant")) and process.poll() is None and time.monotonic() < deadline:
                            time.sleep(0.01)
                        self.assertTrue(started.exists(), "disposable startup stage did not start")
                        pids = [int(started.read_text()), int((root / "descendant").read_text())]
                        process.send_signal(sig)
                        stdout, stderr = process.communicate(timeout=8)
                        deadline = time.monotonic() + 1
                        while any(process_running(pid) for pid in pids) and time.monotonic() < deadline:
                            time.sleep(0.01)
                        self.assertFalse(any(process_running(pid) for pid in pids), "startup process survived cancellation")
                        self.assertEqual(process.returncode, 0, "launcher must handle startup cancellation normally")
                        if stage == "setup":
                            # Ollama setup progress is shown; build stdout is not.
                            self.assertIn("private fixture output", stdout)
                        else:
                            self.assertNotIn("private fixture output", stdout + stderr)
                    finally:
                        # Only disposable fixture groups/PIDs are signalled.
                        try:
                            os.killpg(process.pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                        for pid in pids:
                            try:
                                os.kill(pid, signal.SIGKILL)
                            except ProcessLookupError:
                                pass
                        process.communicate(timeout=5)

    def test_cancellation_stops_and_reaps_children(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "started"
            command = [sys.executable, "-c", "import pathlib,time; pathlib.Path(__import__('sys').argv[1]).touch(); time.sleep(60)", str(path)]
            stop = threading.Event()
            timer = threading.Timer(0.3, stop.set)
            timer.start()
            try:
                children = dev.run_children([command], [path.exists], stop)
            finally:
                timer.cancel()
            self.assertTrue(path.exists())
            self.assertTrue(all(p.poll() is not None for p in children))

    def test_early_failure_stops_sibling(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "started"
            sleeper = [sys.executable, "-c", "import pathlib,time; pathlib.Path(__import__('sys').argv[1]).touch(); time.sleep(60)", str(path)]
            failure = [sys.executable, "-c", "raise SystemExit(7)"]
            with self.assertRaisesRegex(RuntimeError, "exited during startup"):
                dev.run_children([sleeper, failure], [path.exists, lambda: False], threading.Event())
            self.assertTrue(path.exists())

    def test_startup_failure_shows_child_stderr(self):
        failure = [sys.executable, "-c", "import sys; print('canvas: cannot open HTTP listener: address already in use', file=sys.stderr); sys.exit(1)"]
        with self.assertRaisesRegex(RuntimeError, "address already in use"):
            dev.run_children([failure], [lambda: False], threading.Event())

    def test_main_prints_runtime_error_message(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / "scripts").mkdir()
            (root / "scripts" / "dev.py").write_text(pathlib.Path(dev.__file__).read_text())
            (root / "model.config.json").write_text('{"profile":"hosted"}')
            result = subprocess.run([sys.executable, str(root / "scripts" / "dev.py")], capture_output=True, text=True, timeout=10,
                                    env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1"))
        self.assertEqual(result.returncode, 1)
        self.assertIn("endpoint", result.stderr)

    def test_readiness_timeout_is_bounded_and_reaped(self):
        sleeper = [sys.executable, "-c", "import time; time.sleep(60)"]
        with self.assertRaisesRegex(RuntimeError, "startup timed out"):
            dev.run_children([sleeper], [lambda: False], threading.Event(), startup_timeout=0.1)

    def test_stubborn_child_is_killed_after_grace_period(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "started"
            command = [sys.executable, "-c", "import pathlib,signal,time,sys; signal.signal(signal.SIGTERM, signal.SIG_IGN); pathlib.Path(sys.argv[1]).touch(); time.sleep(60)", str(path)]
            stop = threading.Event()
            timer = threading.Timer(0.3, stop.set)
            timer.start()
            try:
                children = dev.run_children([command], [path.exists], stop, shutdown_timeout=0.1)
            finally:
                timer.cancel()
            self.assertEqual(children[0].returncode, -9)

    def test_preflight_allows_restart_after_server_time_wait(self):
        with socket.socket() as listener:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
            listener.listen()
            with socket.create_connection(("127.0.0.1", port)) as client:
                accepted, _ = listener.accept()
                with accepted:
                    accepted.shutdown(socket.SHUT_WR)
                    self.assertEqual(client.recv(1), b"")
                    client.close()
                    self.assertEqual(accepted.recv(1), b"")
        dev.check_listeners([port])

    def test_preflight_refuses_an_occupied_listener(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            listener.listen()
            with self.assertRaisesRegex(RuntimeError, "listener unavailable"):
                dev.check_listeners([listener.getsockname()[1]])

    def test_process_group_cleanup_stops_descendants(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "grandchild"
            code = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-c','import pathlib,sys,time; time.sleep(0.8); pathlib.Path(sys.argv[1]).touch()',sys.argv[1]]); time.sleep(60)"
            stop = threading.Event()
            timer = threading.Timer(0.3, stop.set)
            timer.start()
            try:
                dev.run_children([[sys.executable, "-c", code, str(path)]], [lambda: True], stop)
            finally:
                timer.cancel()
            time.sleep(1)
            self.assertFalse(path.exists())


if __name__ == "__main__":
    unittest.main()
