#!/usr/bin/env python3
"""Run one deterministic, process-isolated shard of the Canvas race tests."""
import argparse
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

PACKAGE = "github.com/agent-primitives/canvas/internal/canvas"


def test_names(output):
    names = []
    for line in output.splitlines():
        if re.fullmatch(r"(?:Test|Example|Fuzz)\w*", line):
            names.append(line)
        elif not re.fullmatch(r"ok\s+" + re.escape(PACKAGE) + r"\s+.*", line):
            raise ValueError(f"unexpected test inventory line: {line}")
    if not names or len(names) != len(set(names)):
        raise ValueError("empty or duplicate compiled test inventory")
    return sorted(names)


def partition(names, shard, shards):
    if shards < 1 or not 0 <= shard < shards:
        raise ValueError("shard index must be in [0, shards)")
    selected = sorted(name for name in names
                      if int.from_bytes(hashlib.sha256(name.encode()).digest(), "big") % shards == shard)
    if not selected:
        raise ValueError("empty Canvas test shard")
    return selected


def summarize(path, selected):
    tests, package = {}, None
    for line in path.read_text().splitlines():
        event = json.loads(line)
        if event.get("Package") != PACKAGE:
            continue
        action, name = event.get("Action"), event.get("Test")
        if action == "fail":
            raise ValueError(f"Canvas test failed: {name or PACKAGE}")
        if name and "/" not in name and action in ("pass", "skip"):
            if name in tests:
                raise ValueError(f"duplicate terminal test event: {name}")
            tests[name] = {"name": name, "action": action, "seconds": event.get("Elapsed", 0)}
        elif not name and action in ("pass", "skip"):
            package = event
    if set(tests) != set(selected):
        raise ValueError(f"test inventory mismatch: missing={sorted(set(selected) - tests.keys())} "
                         f"unexpected={sorted(tests.keys() - set(selected))}")
    if package is None or package["Action"] != "pass" or not any(t["action"] == "pass" for t in tests.values()):
        raise ValueError("Canvas shard needs a passing package and at least one passing top-level test")
    return {"package_seconds": package["Elapsed"], "tests": sorted(tests.values(), key=lambda t: t["name"])}


def run(shard, shards, output):
    names = test_names(subprocess.check_output(
        ["go", "test", "-race", PACKAGE, "-list", "^(Test|Example|Fuzz)"], text=True))
    selected = partition(names, shard, shards)
    pattern = "^(" + "|".join(re.escape(name) for name in selected) + ")$"
    print(f"Canvas shard {shard}/{shards}: {len(selected)} of {len(names)} top-level tests", flush=True)
    print(f"JSON log: {output}", flush=True)
    with output.open("w") as stream:
        process = subprocess.run(["go", "test", "-race", PACKAGE, "-json", "-count=1", "-timeout=600s", "-run", pattern],
                                 stdout=stream, stderr=subprocess.STDOUT, check=False)
    try:
        result = summarize(output, selected)
    except ValueError as error:
        print(error, file=sys.stderr)
        print(output.read_text(), file=sys.stderr)
        return process.returncode or 1
    result.update(shard=shard, shards=shards, inventory_count=len(names))
    output.with_suffix(".timings.json").write_text(json.dumps(result, indent=2) + "\n")
    print(f"Canvas shard package: {result['package_seconds']:.3f}s")
    for test in sorted(result["tests"], key=lambda t: t["seconds"], reverse=True)[:20]:
        print(f"{test['seconds']:8.3f}s {test['action']:4} {test['name']}")
    return process.returncode


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--shard", type=int, required=True)
    parser.add_argument("--shards", type=int, default=4)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        return run(args.shard, args.shards, args.output)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(error, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
