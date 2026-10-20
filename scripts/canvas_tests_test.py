import importlib.util
import json
import os
import shlex
import subprocess
import sys
import tempfile
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
            # This executable stands in for the supplied PYTHON interpreter. All
            # four must start before any finishes, so serial recipes fail.
            python = work / "python"
            python.write_text(f"#!{sys.executable}\n" + '''import os, pathlib, shlex, sys, time
root = pathlib.Path(os.environ["CAPTURE"])
args = sys.argv[1:]
shard = args[args.index("--shard") + 1]
(root / ("started-" + shard)).write_text(shlex.join(args))
end = time.monotonic() + 3
while len(list(root.glob("started-*"))) != 4:
    if time.monotonic() > end:
        sys.exit(9)
    time.sleep(.01)
if shard == "3":
    time.sleep(.15)
(root / ("finished-" + shard)).touch()
print("finished " + shard)
sys.exit(7 if shard == "1" and os.environ["FAIL_SHARD"] == "1" else 0)
''')
            python.chmod(0o755)
            go = work / "go"
            go.write_text(f"#!{sys.executable}\n" + '''import json, os, pathlib, sys
root = pathlib.Path(os.environ["CAPTURE"])
if sys.argv[1] == "list":
    print(os.environ["LISTING"])
    sys.exit(int(os.environ["LIST_STATUS"]))
(root / "go-tests").write_text(json.dumps(sys.argv[1:]))
sys.exit(int(os.environ["TEST_STATUS"]))
''')
            go.chmod(0o755)
            environment = {k: v for k, v in os.environ.items() if k not in ("MAKEFLAGS", "MFLAGS", "MAKELEVEL")}
            environment.update(PATH=directory + os.pathsep + os.environ["PATH"], TMPDIR=directory,
                               CAPTURE=directory, FAIL_SHARD=str(int(fail_shard)),
                               LISTING=listing if listing is not None else PACKAGE + "\nother/package\n" + PACKAGE + "/extra",
                               LIST_STATUS=str(list_status), TEST_STATUS=str(test_status))
            result = subprocess.run(["make", "--no-print-directory", target, f"PYTHON={python}"],
                                    cwd=ROOT, env=environment, capture_output=True, text=True, timeout=10, check=False)
            calls = [shlex.split(p.read_text()) for p in sorted(work.glob("started-*"))]
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
                outputs = []
                for index, call in enumerate(calls):
                    self.assertEqual(call[:5], ["scripts/canvas-tests.py", "--shard", str(index), "--shards", "4"])
                    output = Path(call[call.index("--output") + 1])
                    self.assertIn(str(output.parent), result.stdout)
                    self.assertIn(f"finished {index}", result.stdout)
                    outputs.append(output)
                self.assertEqual(len(set(outputs)), 4)

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


if __name__ == "__main__":
    unittest.main()
