import contextlib
import importlib.util
import io
import json
import pathlib
import subprocess
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("canvas_ollama", pathlib.Path(__file__).with_name("ollama.py"))
ollama = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ollama)


class OllamaPortTest(unittest.TestCase):
    def setUp(self):
        ollama.configure(11434)
        ollama.ENGINE = "docker"

    def test_override_launches_distinct_owned_resources_on_loopback(self):
        actions = []
        ollama.configure(12434)
        with patch.object(ollama, "owned", return_value=False), patch.object(ollama, "container", side_effect=lambda *args: actions.append(args)), patch.object(ollama, "inventory", return_value=[]), patch.object(ollama, "verify"):
            ollama.start()
        launch = next(args for args in actions if args[0] == "run")
        self.assertEqual(launch[launch.index("-p") + 1], "127.0.0.1:12434:11434")
        self.assertEqual(launch[launch.index("--name") + 1], "canvas-draft-room-ollama-12434")
        self.assertEqual(ollama.LOCK["baseURL"], "http://127.0.0.1:12434")
        self.assertTrue(any("canvas-draft-room-models-12434" in args for args in actions))

    def test_manifest_url_follows_the_locked_model(self):
        self.assertEqual(ollama.manifest_url("qwen3:8b"), "https://registry.ollama.ai/v2/library/qwen3/manifests/8b")
        self.assertEqual(ollama.manifest_url("acme/tiny"), "https://registry.ollama.ai/v2/acme/tiny/manifests/latest")
        self.assertEqual(ollama.manifest_url(ollama.LOCK["model"]).split("/v2/")[1],
                         "library/%s/manifests/%s" % tuple(ollama.LOCK["model"].split(":")))

    def test_stopped_owned_container_uses_configured_loopback_binding(self):
        ollama.configure(12434)
        actions = []

        def stopped_container(*args, **kwargs):
            actions.append(args)
            if args[0] == "port":
                raise RuntimeError("runtime port mapping unavailable while stopped")
            if args[-1] == "{{.Config.Image}}":
                return subprocess.CompletedProcess(args, 0, ollama.LOCK["image"], "")
            if args[-1] == "{{json .HostConfig.PortBindings}}":
                binding = {"11434/tcp": [{"HostIp": "127.0.0.1", "HostPort": "12434"}]}
                return subprocess.CompletedProcess(args, 0, json.dumps(binding), "")
            return subprocess.CompletedProcess(args, 0, "", "")

        with patch.object(ollama, "owned", return_value=True), patch.object(ollama, "container", side_effect=stopped_container), patch.object(ollama, "inventory", return_value=[]), patch.object(ollama, "verify"):
            ollama.start()
        self.assertIn(("start", ollama.NAME), actions)

    def test_invalid_ports_refused(self):
        for port in (0, -1, 65536):
            with self.assertRaises(RuntimeError):
                ollama.configure(port)


class OllamaRemovalTest(unittest.TestCase):
    def setUp(self):
        ollama.configure(11434)
        ollama.ENGINE = "docker"

    def test_remove_refuses_unavailable_daemon_without_reporting_success(self):
        for unavailable in ("container", "volume"):
            with self.subTest(kind=unavailable):
                output = io.StringIO()
                actions = []

                def unavailable_docker(command, **kwargs):
                    args = command[1:]
                    actions.append(args)
                    if args[:2] == ["container", "inspect"] and unavailable == "volume":
                        return subprocess.CompletedProcess(command, 0, json.dumps({ollama.KEY: ollama.VALUE}), "")
                    if args[:2] == ["rm", "-f"]:
                        return subprocess.CompletedProcess(command, 0, "", "")
                    return subprocess.CompletedProcess(command, 1, "", "private transport fixture")

                with patch.object(ollama.subprocess, "run", side_effect=unavailable_docker), contextlib.redirect_stdout(output):
                    with self.assertRaises(RuntimeError) as raised:
                        ollama.remove()
                self.assertNotIn("private transport fixture", str(raised.exception))
                self.assertEqual(output.getvalue(), "")
                self.assertFalse(any(args[:2] == ["volume", "rm"] for args in actions))
                if unavailable == "container":
                    self.assertFalse(any(args[:2] == ["rm", "-f"] for args in actions))

    def test_remove_succeeds_when_named_resources_are_confirmed_absent(self):
        actions = []

        def absent_docker(command, **kwargs):
            args = command[1:]
            actions.append(args)
            if args[:2] in (["container", "inspect"], ["volume", "inspect"]):
                return subprocess.CompletedProcess(command, 1, "", "private transport fixture")
            if args == ["container", "ls", "--all", "--format", "{{.Names}}"]:
                return subprocess.CompletedProcess(command, 0, "unrelated-container\n", "")
            if args == ["volume", "ls", "--format", "{{.Name}}"]:
                return subprocess.CompletedProcess(command, 0, "unrelated-volume\n", "")
            self.fail("unexpected Docker action for absent resources")

        output = io.StringIO()
        with patch.object(ollama.subprocess, "run", side_effect=absent_docker), contextlib.redirect_stdout(output):
            ollama.remove()
        self.assertIn("resources are absent from Docker", output.getvalue())
        self.assertEqual(len(actions), 4)

    def test_inspection_failure_is_not_absence_when_listing_contains_resource(self):
        for kind, name in (("container", ollama.NAME), ("volume", ollama.VOLUME)):
            with self.subTest(kind=kind):
                def present_docker(command, **kwargs):
                    if command[2] == "inspect":
                        return subprocess.CompletedProcess(command, 1, "", "private transport fixture")
                    return subprocess.CompletedProcess(command, 0, name + "\n", "")

                with patch.object(ollama.subprocess, "run", side_effect=present_docker):
                    with self.assertRaises(RuntimeError):
                        ollama.owned(kind, name)

    def test_remove_refuses_foreign_labels(self):
        output = io.StringIO()
        result = subprocess.CompletedProcess([], 0, json.dumps({ollama.KEY: "foreign"}), "")
        with patch.object(ollama.subprocess, "run", return_value=result), contextlib.redirect_stdout(output):
            with self.assertRaises(RuntimeError):
                ollama.remove()
        self.assertEqual(output.getvalue(), "")


class ContainerEngineTest(unittest.TestCase):
    def setUp(self):
        ollama.configure(11434)
        ollama.ENGINE = None

    def select(self, requested, readiness, installed=("docker", "podman"), resources=()):
        output = io.StringIO()
        with patch.object(ollama, "engine_ready", side_effect=lambda engine: readiness.get(engine, False)), \
             patch.object(ollama, "has_named_resources", side_effect=lambda engine: engine in resources), \
             patch.object(ollama.shutil, "which", side_effect=lambda engine: "/usr/bin/" + engine if engine in installed else None), \
             contextlib.redirect_stdout(output):
            selected = ollama.select_engine(requested)
        return selected, output.getvalue()

    def test_auto_prefers_ready_docker(self):
        selected, output = self.select("auto", {"docker": True, "podman": True})
        self.assertEqual(selected, "docker")
        self.assertEqual(ollama.ENGINE, "docker")
        self.assertIn("Using Docker", output)

    def test_auto_falls_back_to_ready_podman(self):
        calls = []
        with patch.object(ollama, "engine_ready", side_effect=lambda engine: calls.append(engine) or engine == "podman"), \
             patch.object(ollama, "has_named_resources", return_value=False), patch.object(ollama.shutil, "which", side_effect=lambda engine: None if engine == "docker" else "/usr/bin/podman"), \
             contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(ollama.select_engine("auto"), "podman")
        self.assertEqual(calls, ["docker", "podman"])

    def test_auto_reuses_the_only_engine_with_existing_canvas_resources(self):
        selected, _ = self.select("auto", {"docker": True, "podman": True}, resources=("podman",))
        self.assertEqual(selected, "podman")

    def test_auto_requires_choice_when_both_engines_have_canvas_resources(self):
        with patch.object(ollama, "engine_ready", return_value=True), patch.object(ollama, "has_named_resources", return_value=True), \
             patch.object(ollama, "same_resource_store", return_value=False), \
             self.assertRaisesRegex(RuntimeError, "select CONTAINER_ENGINE explicitly"):
            ollama.select_engine("auto")

    def test_auto_accepts_two_commands_for_the_same_resource_store(self):
        with patch.object(ollama, "engine_ready", return_value=True), patch.object(ollama, "has_named_resources", return_value=True), \
             patch.object(ollama, "same_resource_store", return_value=True), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(ollama.select_engine("auto"), "docker")

    def test_auto_uses_ready_engine_when_another_installed_engine_is_unavailable(self):
        with patch.object(ollama, "engine_ready", side_effect=lambda engine: engine == "docker"), \
             patch.object(ollama, "has_named_resources", return_value=False), patch.object(ollama.shutil, "which", return_value="/usr/bin/engine"), \
             contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(ollama.select_engine("auto"), "docker")

    def test_explicit_unready_engine_never_falls_back(self):
        calls = []
        with patch.object(ollama, "engine_ready", side_effect=lambda engine: calls.append(engine) or False), \
             patch.object(ollama.shutil, "which", return_value="/usr/bin/docker"), \
             patch.object(ollama.sys, "platform", "darwin"), \
             self.assertRaisesRegex(RuntimeError, "open -a Docker") as failure:
            ollama.select_engine("docker")
        self.assertEqual(calls, ["docker"])
        self.assertIn("never invokes", str(failure.exception))

    def test_missing_explicit_engine_reports_install_requirement(self):
        with patch.object(ollama, "engine_ready", return_value=False), patch.object(ollama.shutil, "which", return_value=None), \
             self.assertRaisesRegex(RuntimeError, "not installed or not on PATH"):
            ollama.select_engine("podman")

    def test_probe_suppresses_output_and_handles_timeout(self):
        with patch.object(ollama.shutil, "which", return_value="/usr/bin/docker"), patch.object(
                ollama.subprocess, "run", side_effect=subprocess.TimeoutExpired(["docker", "info"], 10)) as run:
            self.assertFalse(ollama.engine_ready("docker"))
        self.assertEqual(run.call_args.args[0], ["docker", "info"])
        self.assertIs(run.call_args.kwargs["stdout"], subprocess.DEVNULL)
        self.assertIs(run.call_args.kwargs["stderr"], subprocess.DEVNULL)

    def test_container_uses_selected_engine_and_hides_transport_errors(self):
        ollama.ENGINE = "podman"
        result = subprocess.CompletedProcess([], 1, "", "private transport fixture")
        with patch.object(ollama.subprocess, "run", return_value=result) as run, self.assertRaises(RuntimeError) as failure:
            ollama.container("info")
        self.assertEqual(run.call_args.args[0], ["podman", "info"])
        self.assertNotIn("private transport fixture", str(failure.exception))

    def test_named_resource_probe_requires_both_safe_listings(self):
        responses = [subprocess.CompletedProcess([], 0, ollama.NAME + "\n", ""),
                     subprocess.CompletedProcess([], 0, "unrelated\n", "")]
        with patch.object(ollama, "engine_command", side_effect=responses) as command:
            self.assertTrue(ollama.has_named_resources("podman"))
        self.assertEqual(command.call_args_list[0].args[:3], ("podman", "container", "ls"))
        self.assertEqual(command.call_args_list[1].args[:3], ("podman", "volume", "ls"))
        responses[0] = subprocess.CompletedProcess([], 1, "", "private transport fixture")
        with patch.object(ollama, "engine_command", side_effect=responses), self.assertRaises(RuntimeError) as failure:
            ollama.has_named_resources("docker")
        self.assertNotIn("private transport fixture", str(failure.exception))

    def test_same_store_uses_immutable_resource_identity(self):
        with patch.object(ollama, "resource_store_identity", side_effect=["container:abc", "container:abc"]):
            self.assertTrue(ollama.same_resource_store(["docker", "podman"]))
        with patch.object(ollama, "resource_store_identity", side_effect=["container:abc", "container:def"]):
            self.assertFalse(ollama.same_resource_store(["docker", "podman"]))

    def test_resource_identity_falls_back_to_volume_mountpoint(self):
        responses = [subprocess.CompletedProcess([], 1, "", "private transport fixture"),
                     subprocess.CompletedProcess([], 0, "/private/store\n", "")]
        with patch.object(ollama, "engine_command", side_effect=responses):
            self.assertEqual(ollama.resource_store_identity("podman"), "volume:/private/store")

    def test_podman_uses_fully_qualified_pinned_image(self):
        ollama.ENGINE = "podman"
        self.assertEqual(ollama.image_reference(), "docker.io/" + ollama.LOCK["image"])
        actions = []
        with patch.object(ollama, "owned", return_value=False), patch.object(
                ollama, "container", side_effect=lambda *args: actions.append(args)), \
             patch.object(ollama, "inventory", return_value=[]), patch.object(ollama, "verify"):
            ollama.start()
        launch = next(args for args in actions if args[0] == "run")
        self.assertEqual(launch[-1], "docker.io/" + ollama.LOCK["image"])

    def test_docker_keeps_locked_image_reference(self):
        ollama.ENGINE = "docker"
        self.assertEqual(ollama.image_reference(), "docker.io/" + ollama.LOCK["image"])


if __name__ == "__main__":
    unittest.main()
