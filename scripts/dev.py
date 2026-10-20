#!/usr/bin/env python3
"""Run the Northwind Canvas with embedded local Sessions; Ctrl-C stops it."""
import argparse
import json
import os
import pathlib
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import time

ROOT = pathlib.Path(__file__).resolve().parents[1]


class StopFlag:
    """Signal handlers must not acquire threading.Event's non-reentrant lock."""
    def __init__(self):
        self.requested = False

    def set(self):
        self.requested = True

    def is_set(self):
        return self.requested

    def wait(self, seconds):
        if not self.requested:
            time.sleep(seconds)
        return self.requested


def check_listeners(ports):
    if len(set(ports)) != len(ports) or any(not 1 <= p <= 65535 for p in ports):
        raise RuntimeError("Distinct loopback ports between 1 and 65535 required.")
    for port in ports:
        try:
            with socket.socket() as listener:
                # Match Go's listener semantics so TCP TIME_WAIT does not block restart.
                listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                listener.bind(("127.0.0.1", port))
        except OSError:
            raise RuntimeError("Loopback listener unavailable; choose an explicit port override.") from None


def reachable(port):
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=0.2):
            return True
    except OSError:
        return False


def stop_children(children, timeout):
    # Each child owns a process group so descendants cannot survive the launcher.
    for child in children:
        try:
            os.killpg(child.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    deadline = time.monotonic() + timeout
    for child in children:
        try:
            child.wait(timeout=max(0, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            pass
    # Kill remaining group members even when the group's leader already exited.
    for child in children:
        try:
            os.killpg(child.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        child.wait()


def run_command(command, stop, shutdown_timeout=5, show_output=False):
    """Run a finite startup step with the same group cleanup as runtime children.

    stderr always reaches the user; stdout only with show_output."""
    if stop.is_set():
        return None
    children = []
    try:
        stdout = None if show_output else subprocess.DEVNULL
        child = subprocess.Popen(command, cwd=ROOT, start_new_session=True, stdout=stdout, stderr=None)
        children.append(child)
        while not stop.is_set():
            result = child.poll()
            if result is not None:
                return result
            stop.wait(0.05)
        return None
    finally:
        stop_children(children, shutdown_timeout)


def exit_message(children, logs, when):
    """Describe the first exited child, with the end of its stderr."""
    for child, log in zip(children, logs):
        if child.poll() is not None:
            name = pathlib.Path(str(child.args[0])).name
            log.seek(0, os.SEEK_END)
            log.seek(max(0, log.tell() - 4096))
            detail = log.read().decode("utf-8", "replace").strip()
            message = f"{name} exited {when} (exit code {child.returncode})."
            return message + ("\n" + detail if detail else "")
    return f"Development child exited {when}."


def run_children(commands, readiness, stop, startup_timeout=30, shutdown_timeout=5):
    # Runtime output stays quiet. stderr goes to a temporary file so a child's
    # own error message can be shown if it exits.
    children = []
    logs = []
    try:
        for command, ready in zip(commands, readiness, strict=True):
            if stop.is_set():
                return children
            log = tempfile.TemporaryFile()
            logs.append(log)
            child = subprocess.Popen(command, cwd=ROOT, start_new_session=True, stdout=subprocess.DEVNULL, stderr=log)
            children.append(child)
            deadline = time.monotonic() + startup_timeout
            while not stop.is_set():
                if any(p.poll() is not None for p in children):
                    raise RuntimeError(exit_message(children, logs, "during startup"))
                if ready():
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError("Development startup timed out.")
                stop.wait(0.05)
        while not stop.is_set():
            if any(p.poll() is not None for p in children):
                raise RuntimeError(exit_message(children, logs, "unexpectedly"))
            stop.wait(0.1)
        return children
    finally:
        stop_children(children, shutdown_timeout)
        for log in logs:
            log.close()


HOSTED_FIELDS = ("endpoint", "model", "keyFile", "auth")
HOSTED_MISSING = 'The hosted profile needs "hosted": {"endpoint": ..., "model": ...}.'


def model_profile(override):
    """Read the local full-file override, then shipped config, then offline.

    Only an absent file permits fallback. The hosted block is checked for shape
    here; the Go commands check its values and read the key file. Credentials
    never live in these configuration files."""
    def unique_object(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError(f"duplicate field {key!r}")
            result[key] = value
        return result

    for filename in ("model.config.local.json", "model.config.json"):
        path = ROOT / filename
        try:
            descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        except FileNotFoundError:
            continue
        except OSError:
            raise RuntimeError(f"{path} must be a readable regular file, not a symlink.") from None
        break
    else:
        if override == "hosted":
            raise RuntimeError(f"{path}: {HOSTED_MISSING}") from None
        return override or "offline"
    try:
        with os.fdopen(descriptor, "rb") as config:
            if not stat.S_ISREG(os.fstat(config.fileno()).st_mode):
                raise ValueError("not a regular file")
            data = config.read(16385)
        if len(data) > 16384:
            raise ValueError("larger than 16 KiB")
        settings = json.loads(data.decode("utf-8"), object_pairs_hook=unique_object)
        if not isinstance(settings, dict) or "profile" not in settings:
            raise ValueError('expected an object with a "profile" field')
        unknown = set(settings) - {"profile", "hosted"}
        if unknown:
            raise ValueError(f"unknown field {sorted(unknown)[0]!r}")
        if settings["profile"] not in ("hosted", "offline"):
            raise ValueError('"profile" must be "hosted" or "offline"')
        hosted = settings.get("hosted", {})
        if not isinstance(hosted, dict):
            raise ValueError('"hosted" must be an object')
        unknown = set(hosted) - set(HOSTED_FIELDS)
        if unknown:
            raise ValueError(f"unknown hosted field {sorted(unknown)[0]!r}")
        if any(not isinstance(value, str) for value in hosted.values()):
            raise ValueError("hosted fields must be strings")
    except UnicodeDecodeError:
        raise RuntimeError(f"Invalid {path}: not UTF-8.") from None
    except (OSError, ValueError, RecursionError) as error:
        raise RuntimeError(f"Invalid {path}: {error}.") from None
    profile = override or settings["profile"]
    if profile == "hosted" and not (hosted.get("endpoint") and hosted.get("model")):
        raise RuntimeError(f"{path}: {HOSTED_MISSING}")
    return profile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--http-port", type=int, default=8765)
    parser.add_argument("--model-port", type=int, default=11434, help="offline Ollama loopback port")
    parser.add_argument("--model-profile", choices=("hosted", "offline"), default="",
                        help="override profile from model.config.local.json or model.config.json (both absent defaults to offline)")
    parser.add_argument("--container-engine", choices=("auto", "docker", "podman"), default="auto",
                        help="container engine for offline Ollama; auto uses a running engine")
    args = parser.parse_args()
    profile = model_profile(args.model_profile)
    stop = StopFlag()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stop.set())
    check_listeners([args.http_port])
    if profile == "offline":
        if not 1 <= args.model_port <= 65535 or args.model_port == args.http_port:
            raise RuntimeError("Model port must be distinct and between 1 and 65535.")
        # Only offline selection manages this example's labeled Ollama resources.
        # The selected engine refuses a foreign port owner; no service is stopped.
        # Its progress output is shown: the first model download is ~5.2 GB.
        setup = run_command([sys.executable, str(ROOT / "scripts/ollama.py"), "start", "--port", str(args.model_port),
                             "--container-engine", args.container_engine], stop, show_output=True)
        if setup is None or stop.is_set():
            return
        if setup:
            raise RuntimeError("Ollama setup failed; see the message above.")
    binary = ROOT / ".bin"
    binary.mkdir(exist_ok=True)
    for name in ("canvas", "sessions-host"):
        build = run_command(["go", "build", "-o", str(binary / name), "./cmd/" + name], stop)
        if build is None or stop.is_set():
            return
        if build:
            raise RuntimeError("go build ./cmd/" + name + " failed; see the compiler output above.")
    base = "http://127.0.0.1:" + str(args.model_port)
    common = ["-root", str(ROOT), "-state", str(ROOT / ".state/northwind"),
              "-model-profile", profile, "-model-base", base]
    check = run_command([str(binary / "sessions-host"), *common, "-check-model"], stop, show_output=True)
    if check is None or stop.is_set():
        return
    if check:
        raise RuntimeError("Model endpoint diagnostic failed; check configuration, credentials or reachability (no generation attempted); see the message above.")
    commands = [[str(binary / "canvas"), *common, "-definition", "examples/northwind-prd/canvas.yaml",
                 "-listen", "127.0.0.1:" + str(args.http_port)]]
    print("Canvas (Northwind): http://127.0.0.1:" + str(args.http_port), flush=True)
    print("Starting Canvas with embedded loopback Sessions; Ctrl-C stops it.", flush=True)
    run_children(commands, [lambda: reachable(args.http_port)], stop)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(130)
    except RuntimeError as error:
        print(error, file=sys.stderr)
        sys.exit(1)
    except Exception:
        print("Development startup failed unexpectedly; check model.config.local.json or model.config.json, the offline container engine and ports.", file=sys.stderr)
        sys.exit(1)
