import importlib.util
import json
import os
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/canvas-tests.py"
PACKAGE = "github.com/agent-primitives/canvas/internal/canvas"


class CanvasShardTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if SCRIPT.exists():
            spec = importlib.util.spec_from_file_location("canvas_tests", SCRIPT)
            assert spec is not None and spec.loader is not None
            cls.runner = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(cls.runner)

    def setUp(self):
        self.assertTrue(SCRIPT.exists(), "Canvas shard runner must exist")

    def test_partition_is_stable_disjoint_exhaustive_and_keeps_seed_tests(self):
        names = [f"TestCase{i}" for i in range(100)] + ["ExampleRun", "FuzzDecode"]
        groups = [self.runner.partition(names, i, 4) for i in range(4)]
        flattened = [name for group in groups for name in group]
        self.assertEqual(sorted(flattened), sorted(names))
        self.assertEqual(len(set(flattened)), len(names))
        self.assertTrue(all(groups))
        for i, group in enumerate(groups):
            self.assertEqual([n for n in self.runner.partition(names + ["TestNew"], i, 4)
                              if n != "TestNew"], group)
        self.assertEqual(self.runner.partition(["TestOne"], 3, 4), ["TestOne"])

    def test_discovery_and_partition_refuse_bad_or_empty_inventory(self):
        inventory = f"TestOne\nExampleRun\nFuzzDecode\nok\t{PACKAGE}\t0.1s\n"
        self.assertEqual(self.runner.test_names(inventory), ["ExampleRun", "FuzzDecode", "TestOne"])
        for bad in ("", "TestOne\nTestOne", "compile failed", "TestOne/sub", "BenchmarkRun"):
            with self.subTest(inventory=bad), self.assertRaises(ValueError):
                self.runner.test_names(bad)
        for index, count in ((-1, 4), (4, 4), (0, 0)):
            with self.subTest(index=index, count=count), self.assertRaises(ValueError):
                self.runner.partition(["TestOne"], index, count)
        with self.assertRaises(ValueError):
            self.runner.partition([], 0, 4)

    def event(self, action, test=None):
        event = {"Package": PACKAGE, "Action": action, "Elapsed": 1.25}
        if test:
            event["Test"] = test
        return event

    def summarize(self, events):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "tests.jsonl"
            path.write_text("\n".join(json.dumps(e) if isinstance(e, dict) else e for e in events))
            return self.runner.summarize(path, ["TestOne", "TestOptIn"])

    def test_summary_keeps_existing_skips_and_excludes_output_payloads(self):
        events = [self.event("pass", "TestOne/sub"), self.event("pass", "TestOne"),
                  self.event("skip", "TestOptIn"), self.event("pass")]
        events.insert(0, {**self.event("output", "TestOne"), "Output": "payload canary"})
        result = self.summarize(events)
        self.assertEqual(result["package_seconds"], 1.25)
        self.assertEqual([t["name"] for t in result["tests"]], ["TestOne", "TestOptIn"])
        self.assertNotIn("payload canary", json.dumps(result))

    def test_summary_refuses_failures_missing_duplicate_unexpected_or_noop_results(self):
        good = [self.event("pass", "TestOne"), self.event("skip", "TestOptIn"), self.event("pass")]
        for events in (good[:-1], good[1:], good + [self.event("pass", "TestUnexpected")],
                       good + [self.event("pass", "TestOne")],
                       good + [self.event("fail", "TestOne/sub")],
                       [self.event("skip", "TestOne"), *good[1:]],
                       [*good[:-1], self.event("fail")], [*good, "build failed"]):
            with self.subTest(events=events), self.assertRaises(ValueError):
                self.summarize(events)

    def test_run_uses_race_exact_parent_selection_and_preserves_go_exit(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "canvas.jsonl"
            with patch.object(self.runner.subprocess, "check_output", side_effect=subprocess.CalledProcessError(2, ["go"])), \
                 self.assertRaises(subprocess.CalledProcessError):
                self.runner.run(0, 1, output)

            def execute(command, **kwargs):
                for event in (self.event("pass", "TestOne"), self.event("pass")):
                    kwargs["stdout"].write(json.dumps(event) + "\n")
                return subprocess.CompletedProcess(command, 7)

            with patch.object(self.runner.subprocess, "check_output", return_value=f"TestOne\nok\t{PACKAGE}\t0s\n") as discover, \
                 patch.object(self.runner.subprocess, "run", side_effect=execute) as execute:
                self.assertEqual(self.runner.run(0, 1, output), 7)
                self.assertIn("-race", discover.call_args.args[0])
                self.assertIn("^(Test|Example|Fuzz)", discover.call_args.args[0])
                command = execute.call_args.args[0]
                for flag in ("-race", "-json", "-count=1", "-timeout=600s", "^(TestOne)$"):
                    self.assertIn(flag, command)


class CanvasMakeTests(unittest.TestCase):
    def make(self, target, *, fail_shard=False, listing=None, list_status=0, test_status=0):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            # Exercise the real runner through the supplied interpreter, with
            # only Go replaced. All four test commands must overlap.
            python = work / "python"
            python.write_text(f"#!{sys.executable}\n" + '''import os, pathlib, sys
(pathlib.Path(os.environ["CAPTURE"]) / "python-call").touch()
os.execv(sys.executable, [sys.executable, *sys.argv[1:]])
''')
            python.chmod(0o755)
            go = work / "go"
            go.write_text(f"#!{sys.executable}\n" + '''import json, os, pathlib, sys, time
root = pathlib.Path(os.environ["CAPTURE"])
package = "github.com/agent-primitives/canvas/internal/canvas"
if sys.argv[1] == "list":
    print(os.environ["LISTING"])
    sys.exit(int(os.environ["LIST_STATUS"]))
if "-list" in sys.argv:
    print("\\n".join("TestCase" + str(i) for i in range(100)))
    print("ok\\t" + package + "\\t0.1s")
    sys.exit(0)
if "-run" not in sys.argv:
    (root / "go-tests").write_text(json.dumps(sys.argv[1:]))
    sys.exit(int(os.environ["TEST_STATUS"]))
(root / ("started-" + str(os.getpid()))).write_text(json.dumps(sys.argv[1:]))
end = time.monotonic() + 5
while len(list(root.glob("started-*"))) != 4:
    if time.monotonic() > end:
        sys.exit(9)
    time.sleep(.01)
selected = sys.argv[sys.argv.index("-run") + 1][2:-2].split("|")
if "TestCase1" in selected:
    time.sleep(.15)
for name in selected:
    print(json.dumps(dict(Package=package, Action="pass", Test=name, Elapsed=.01)))
print(json.dumps(dict(Package=package, Action="pass", Elapsed=.1)))
(root / ("finished-" + str(os.getpid()))).touch()
sys.exit(7 if "TestCase0" in selected and os.environ["FAIL_SHARD"] == "1" else 0)
''')
            go.chmod(0o755)
            environment = {k: v for k, v in os.environ.items() if k not in ("MAKEFLAGS", "MFLAGS", "MAKELEVEL")}
            environment.update(PATH=directory + os.pathsep + os.environ["PATH"], TMPDIR=directory,
                               CAPTURE=directory, FAIL_SHARD=str(int(fail_shard)),
                               LISTING=listing if listing is not None else PACKAGE + "\nother/package\n" + PACKAGE + "/extra",
                               LIST_STATUS=str(list_status), TEST_STATUS=str(test_status))
            result = subprocess.run(["make", "--no-print-directory", target, f"PYTHON={python}"],
                                    cwd=ROOT, env=environment, capture_output=True, text=True, timeout=15, check=False)
            self.assertTrue((work / "python-call").exists(), "Make must honor the PYTHON override")
            calls = [json.loads(p.read_text()) for p in sorted(work.glob("started-*"))]
            finished = [p.name for p in work.glob("finished-*")]
            go_args = json.loads((work / "go-tests").read_text()) if (work / "go-tests").exists() else None
            return result, calls, finished, go_args

    def test_all_four_shards_overlap_and_make_waits_for_every_child_even_on_failure(self):
        for failed in (False, True):
            with self.subTest(failed=failed):
                result, calls, finished, _ = self.make("test-canvas", fail_shard=failed)
                self.assertEqual(result.returncode == 0, not failed, result.stdout + result.stderr)
                self.assertEqual(len(calls), 4)
                self.assertEqual(len(finished), 4)
                selected = []
                for call in calls:
                    for flag in ("-race", "-json", "-count=1", "-timeout=600s"):
                        self.assertIn(flag, call)
                    selected.extend(call[call.index("-run") + 1][2:-2].split("|"))
                self.assertEqual(sorted(selected), sorted(f"TestCase{i}" for i in range(100)))
                outputs = [Path(line.removeprefix("JSON log: ")) for line in result.stdout.splitlines()
                           if line.startswith("JSON log: ")]
                self.assertEqual(len(set(outputs)), 4)
                for output in outputs:
                    self.assertIn(str(output.parent), result.stdout)

    def test_go_target_excludes_only_canvas_and_propagates_failures(self):
        result, _, _, args = self.make("test-go")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(args, ["test", "-race", "other/package", PACKAGE + "/extra"])
        for settings in ({"list_status": 3}, {"listing": PACKAGE}, {"test_status": 7}, {"fail_shard": True}):
            with self.subTest(settings=settings):
                result, _, _, args = self.make("test-go", **settings)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                if "test_status" not in settings:
                    self.assertIsNone(args)


class CanvasCancellationTests(unittest.TestCase):
    def test_signals_stop_all_shards_and_their_descendants(self):
        for interrupt in (signal.SIGINT, signal.SIGTERM):
            with self.subTest(signal=interrupt), tempfile.TemporaryDirectory() as directory:
                work = Path(directory)
                go = work / "go"
                go.write_text(f"#!{sys.executable}\n" + '''import os, pathlib, subprocess, sys
root = pathlib.Path(os.environ["CAPTURE"])
(root / ("pid-" + str(os.getpid()))).touch()
child = subprocess.Popen([sys.executable, "-c", ''' + repr('''import os, pathlib, signal, time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
(pathlib.Path(os.environ["CAPTURE"]) / ("pid-" + str(os.getpid()))).touch()
time.sleep(60)
''') + '''])
child.wait()
''')
                go.chmod(0o755)
                environment = {k: v for k, v in os.environ.items() if k not in ("MAKEFLAGS", "MFLAGS", "MAKELEVEL")}
                environment.update(PATH=directory + os.pathsep + os.environ["PATH"],
                                   TMPDIR=directory, CAPTURE=directory)
                process = subprocess.Popen(["make", "--no-print-directory", "test-canvas", f"PYTHON={sys.executable}"],
                                           cwd=ROOT, env=environment, stdout=subprocess.PIPE,
                                           stderr=subprocess.STDOUT, start_new_session=True, text=True)
                try:
                    deadline = time.monotonic() + 10
                    while len(list(work.glob("pid-*"))) < 8 and time.monotonic() < deadline:
                        time.sleep(.01)
                    pids = [int(p.name.removeprefix("pid-")) for p in work.glob("pid-*")]
                    self.assertEqual(len(pids), 8, "all four discovery processes and descendants must start")
                    os.killpg(process.pid, interrupt)
                    output, _ = process.communicate(timeout=8)
                    self.assertNotEqual(process.returncode, 0, output)
                    deadline = time.monotonic() + 2
                    while any(self.alive(pid) for pid in pids) and time.monotonic() < deadline:
                        time.sleep(.01)
                    self.assertFalse([pid for pid in pids if self.alive(pid)],
                                     "shard descendants survived cancellation")
                finally:
                    # Both the old broken recipe and the fixed isolated groups
                    # are owned by this test; never leave a failed repro running.
                    groups = {process.pid}
                    for recorded in work.glob("pid-*"):
                        try:
                            groups.add(os.getpgid(int(recorded.name.removeprefix("pid-"))))
                        except ProcessLookupError:
                            pass
                    for group in groups:
                        try:
                            os.killpg(group, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                    process.communicate(timeout=5)

    @staticmethod
    def alive(pid):
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return False
        # A killed grandchild may await its reparented reaper on Linux; it is
        # no longer executing. Other POSIX systems reap these through init.
        status = Path(f"/proc/{pid}/stat")
        try:
            return not status.exists() or status.read_text().split(")", 1)[1].split()[0] != "Z"
        except FileNotFoundError:
            return False


if __name__ == "__main__":
    unittest.main()
