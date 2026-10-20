#!/usr/bin/env python3
"""Own only the named, labeled Canvas container/volume; never inspect credentials."""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess
import sys
import time
import urllib.request

if sys.version_info < (3, 10):
    sys.exit("ollama.py needs Python 3.10 or newer.")

ROOT = pathlib.Path(__file__).resolve().parents[1]
LOCK = json.loads((ROOT / "model.lock.json").read_text())
NAME, VOLUME = LOCK["container"], LOCK["volume"]
LABEL = LOCK["ownerLabel"]
KEY, VALUE = LABEL.split("=", 1)
PORT = 11434
ENGINE = None


def configure(port):
    global PORT, NAME, VOLUME
    if not 1 <= port <= 65535:
        raise RuntimeError("Model port must be between 1 and 65535.")
    PORT = port
    # Non-default ports get their own container and volume.
    suffix = "" if port == 11434 else "-" + str(port)
    NAME = LOCK["container"] + suffix
    VOLUME = LOCK["volume"] + suffix
    LOCK["baseURL"] = "http://127.0.0.1:" + str(port)


def engine_ready(engine):
    if shutil.which(engine) is None:
        return False
    try:
        result = subprocess.run([engine, "info"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                timeout=10, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return False
    return result.returncode == 0


def engine_hint(engine):
    if sys.platform == "darwin":
        if engine == "docker":
            return "start Docker Desktop with `open -a Docker`"
        return "start the Podman machine with `podman machine start` (run `podman machine init` first if needed)"
    if sys.platform.startswith("linux"):
        if engine == "docker":
            return "start Docker with your service manager (commonly `sudo systemctl start docker`)"
        return "make `podman info` succeed for your user"
    return "start it and make `%s info` succeed" % engine


def select_engine(requested):
    global ENGINE
    if requested not in ("auto", "docker", "podman"):
        raise RuntimeError("Container engine must be auto, docker or podman.")
    candidates = ("docker", "podman") if requested == "auto" else (requested,)
    installed = [engine for engine in candidates if shutil.which(engine) is not None]
    ready = [engine for engine in candidates if engine_ready(engine)]
    if ready:
        selected = ready[0]
        if requested == "auto":
            existing = [engine for engine in ready if has_named_resources(engine)]
            if len(existing) > 1 and not same_resource_store(existing):
                raise RuntimeError("Canvas model resources exist in both Docker and Podman; select CONTAINER_ENGINE explicitly.")
            if existing:
                selected = existing[0]
        ENGINE = selected
        print("Using %s for the offline model." % selected.capitalize(), flush=True)
        return selected
    if requested != "auto" and not installed:
        raise RuntimeError("%s is not installed or not on PATH; install it or select another engine." % requested.capitalize())
    guidance = " or ".join(engine_hint(engine) for engine in installed)
    if not guidance:
        guidance = "install Docker or Podman and make its `info` command succeed"
    raise RuntimeError("No ready container engine; %s. Canvas never invokes host service or VM startup commands automatically." % guidance)


def engine_command(engine, *args, check=True):
    try:
        result = subprocess.run([engine, *args], capture_output=True, text=True)
    except OSError:
        raise RuntimeError("Container action failed; check engine availability and Canvas ownership.") from None
    if check and result.returncode:
        raise RuntimeError("Container action failed; check engine availability and Canvas ownership.")
    return result


def container(*args, check=True):
    if ENGINE not in ("docker", "podman"):
        raise RuntimeError("Container engine was not selected.")
    return engine_command(ENGINE, *args, check=check)


def has_named_resources(engine):
    container_list = engine_command(engine, "container", "ls", "--all", "--format", "{{.Names}}", check=False)
    volume_list = engine_command(engine, "volume", "ls", "--format", "{{.Name}}", check=False)
    if container_list.returncode or volume_list.returncode:
        raise RuntimeError("Cannot inspect container engines safely; select CONTAINER_ENGINE explicitly.")
    return NAME in container_list.stdout.splitlines() or VOLUME in volume_list.stdout.splitlines()


def resource_store_identity(engine):
    container_info = engine_command(engine, "container", "inspect", NAME, "--format", "{{.Id}}", check=False)
    if not container_info.returncode and container_info.stdout.strip():
        return "container:" + container_info.stdout.strip()
    volume_info = engine_command(engine, "volume", "inspect", VOLUME, "--format", "{{.Mountpoint}}", check=False)
    if not volume_info.returncode and volume_info.stdout.strip():
        return "volume:" + volume_info.stdout.strip()
    raise RuntimeError("Cannot identify existing Canvas model resources; select CONTAINER_ENGINE explicitly.")


def same_resource_store(engines):
    identities = [resource_store_identity(engine) for engine in engines]
    return len(set(identities)) == 1


def image_reference():
    image = LOCK["image"]
    repository = image.split("@", 1)[0]
    first = repository.split("/", 1)[0]
    if "." in first or ":" in first or first == "localhost":
        return image
    return "docker.io/" + (image if "/" in repository else "library/" + image)


def owned(kind, name):
    result = container(kind, "inspect", name, "--format", "{{json .Labels}}" if kind == "volume" else "{{json .Config.Labels}}", check=False)
    if result.returncode:
        # Inspect failures also include daemon/permission errors. Confirm absence
        # from names only; transport stderr can contain credentials.
        listing = container(kind, "ls", *(["--all"] if kind == "container" else []), "--format", "{{.Names}}" if kind == "container" else "{{.Name}}", check=False)
        if listing.returncode or name in listing.stdout.splitlines():
            raise RuntimeError("Cannot confirm named resource absence; check engine availability and Canvas ownership.")
        return False
    labels = json.loads(result.stdout) or {}
    if labels.get(KEY) != VALUE:
        raise RuntimeError("Existing named resource is not owned by this demo; refusing changes.")
    return True


def manifest_url(model):
    name, _, tag = model.partition(":")
    if "/" not in name:
        name = "library/" + name
    return "https://registry.ollama.ai/v2/%s/manifests/%s" % (name, tag or "latest")


def inventory():
    with urllib.request.urlopen(LOCK["baseURL"] + "/api/tags", timeout=5) as response:
        return json.load(response)["models"]


def verify():
    expected = LOCK["manifestDigest"].removeprefix("sha256:")
    if not any(m["name"] == LOCK["model"] and m["digest"].removeprefix("sha256:") == expected for m in inventory()):
        raise RuntimeError("Pinned model missing or mismatched; refusing to use a mutable tag.")
    print("Pinned model verified:", LOCK["model"], LOCK["manifestDigest"])


def start():
    if not owned("volume", VOLUME):
        container("volume", "create", "--label", LABEL, VOLUME)
    if not owned("container", NAME):
        container("run", "-d", "--name", NAME, "--label", LABEL, "-p", "127.0.0.1:" + str(PORT) + ":11434", "-v", VOLUME + ":/root/.ollama", image_reference())
    else:
        image = container("inspect", NAME, "--format", "{{.Config.Image}}").stdout.strip()
        # Runtime port mappings disappear when stopped; validate the saved binding.
        ports = json.loads(container("inspect", NAME, "--format", "{{json .HostConfig.PortBindings}}").stdout)
        expected = {"11434/tcp": [{"HostIp": "127.0.0.1", "HostPort": str(PORT)}]}
        if image.removeprefix("docker.io/") != image_reference().removeprefix("docker.io/") or ports != expected:
            raise RuntimeError("Owned container image or binding differs from lock; remove explicitly before setup.")
        container("start", NAME)
    for _ in range(30):
        try:
            inventory()
            break
        except Exception:
            time.sleep(1)
    else:
        raise RuntimeError("Canvas Ollama did not become ready.")
    try:
        verify()
        return
    except RuntimeError:
        pass
    # Validate the registry manifest BEFORE downloading a mutable tag.
    with urllib.request.urlopen(manifest_url(LOCK["model"]), timeout=30) as response:
        manifest = response.read()
    if "sha256:" + hashlib.sha256(manifest).hexdigest() != LOCK["manifestDigest"]:
        raise RuntimeError("Registry tag changed; review/update the model lock deliberately.")
    request = urllib.request.Request(LOCK["baseURL"] + "/api/pull", data=json.dumps({"model": LOCK["model"], "stream": True}).encode(), headers={"Content-Type": "application/json"})
    print("Downloading pinned %s (~5.2 GB); initial setup can be slow." % LOCK["model"], flush=True)
    last = 0
    with urllib.request.urlopen(request, timeout=14400) as response:
        for line in response:
            event = json.loads(line)
            if "error" in event:
                raise RuntimeError("Model download failed; rerun setup to resume.")
            if time.monotonic() - last > 10 and event.get("total"):
                last = time.monotonic()
                print("Model download: %d%%" % (100 * event.get("completed", 0) / event["total"]), flush=True)
    verify()


def remove():
    removed = False
    if owned("container", NAME):
        container("rm", "-f", NAME)
        removed = True
    if owned("volume", VOLUME):
        container("volume", "rm", VOLUME)
        removed = True
    if removed:
        print("Removed Canvas Ollama resources from %s." % ENGINE.capitalize())
    else:
        print("Canvas Ollama resources are absent from %s." % ENGINE.capitalize())


if __name__ == "__main__":
    try:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("mode", nargs="?", default="start", choices=("start", "verify", "remove"))
        parser.add_argument("--port", type=int, default=11434)
        parser.add_argument("--container-engine", choices=("auto", "docker", "podman"), default="auto")
        args = parser.parse_args()
        configure(args.port)
        mode = args.mode
        if mode == "start":
            select_engine(args.container_engine)
            start()
        elif mode == "verify":
            verify()
        elif mode == "remove":
            select_engine(args.container_engine)
            remove()
        else:
            raise RuntimeError("Usage: python3 scripts/ollama.py start|verify|remove")
    except Exception as error:
        # Only locally generated messages may be printed; HTTP/engine errors can
        # carry transport details that don't belong in operational output.
        print(str(error) if isinstance(error, RuntimeError) else "Ollama setup/check failed; inspect local container/network availability.", file=sys.stderr)
        sys.exit(1)
