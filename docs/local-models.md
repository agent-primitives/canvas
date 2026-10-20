# Running with a local or hosted model

[Docs index](README.md) · [Canvas overview](../README.md)

This model-backed path uses fictional Northwind documents and an embedded local
Sessions host. For fixed responses without model setup, use the separate
[Copilot review demo](copilot-review.md). Personas label actions; they are not
sign-in or authorization. All effects described here remain local simulations.

## What you need

- Linux or macOS, Git, Make, and a browser
- Go 1.27.0+; `go.mod` selects toolchain 1.27.2 (automatic download must be allowed)
- Python 3.10+ for the local model and dev helpers
- A ready Docker or Podman engine for the Ollama route below; no cloud account
- Node and Playwright 1.63.0 with Chromium only if you run the test suite, not
  for using Canvas

## Quick start

From a checkout, run:

```sh
make dev MODEL_PROFILE=offline CONTAINER_ENGINE=auto
```

`make dev` prepares the pinned local Ollama/model in offline mode, builds the
commands and checks model endpoint metadata before starting Northwind with
embedded Sessions. The check does not generate an answer. The only runtime child
is Canvas; it owns the embedded host on an OS-assigned internal loopback port.

Open the URL it prints (default http://127.0.0.1:8765). You should see
**Northwind fictional PRD**, its declared documents, and the writer/reviewer.
No agent runs until you explicitly request one or add/change a declared input.
Seeded feedback is context, not a new-file event.

Initial setup needs network access to download dependencies, the pinned Ollama
image and the `qwen3:8b` model (about 5.2 GB). Pins live in
[`model.lock.json`](../model.lock.json); startup verifies the selected offline
manifest. `check-model` reads endpoint metadata only; it does not verify the pin,
run inference, or judge quality.
CPU generation can take several minutes. Ctrl-C stops Northwind and its embedded
host, but the Ollama container and model volume persist for reuse.

Canvas selects a container engine whose `info` check succeeds. It never invokes
Docker Desktop, system-service or Podman-machine startup commands. A
host-configured socket activation policy may still start an engine when its
client connects. The default `CONTAINER_ENGINE=auto` considers ready engines,
prefers one holding existing Canvas model resources, and otherwise prefers
Docker when both are ready:

```sh
make setup-offline CONTAINER_ENGINE=podman
make setup-offline CONTAINER_ENGINE=docker
```

On macOS, start Docker Desktop with `open -a Docker`, or start Podman with
`podman machine start` (`podman machine init` is needed once). On Linux, make
`docker info` or `podman info` work for your user before running Canvas.

Ports busy? Override them:

```sh
make dev MODEL_PROFILE=offline HTTP_PORT=8875 MODEL_PORT=12434
```

The Ollama container and its model volume stick around between runs. Independent
Docker and Podman backends keep separate resources, so use the same engine to
avoid another model download. Compatibility wrappers that address the same
backend are detected from the existing resource identity. `auto` cannot inspect
resources in an unavailable engine; select that engine explicitly after starting
it to guarantee reuse.
`make remove-ollama CONTAINER_ENGINE=podman` (or `docker`) deletes only the
labeled resources Canvas created in that engine.

## Try the Northwind workflow

`make dev` explicitly selects `examples/northwind-prd/canvas.yaml` and uses
separate `.state/northwind` state under the checkout root. Existing legacy
Draft Room state is neither migrated nor reset. `make run` and the flagless
Canvas command select the same Northwind definition and state, but do not
prepare the local model. An explicitly selected legacy state root is refused
before runtime startup. A first default start may add the `northwind` directory
inside `.state`; it does not change existing legacy files or their modes.

For a direct Northwind start against an already configured model endpoint,
`make northwind` selects the same definition and state but does not prepare
Ollama or run the pre-start metadata diagnostic. Use `make setup-offline` and
`make check-model` first, or configure an existing hosted endpoint below.

1. **New… → New agent run → PRD writer**: request a fictional pilot PRD and
   announcement draft. The model may need another explicit attempt; output must
   satisfy the declared schemas and scopes before it becomes a proposal.
2. **Review affected documents**: inspect every replacement, then Accept or
   Reject the whole proposal. Agents cannot accept their own edits.
3. Explicitly request **Independent reviewer** after accepting the writer's
   proposal. It reads the accepted PRD, not the pending draft.
4. **New… → Add source file** adds validated feedback. A new or changed input can
   trigger a model call; hosted calls may be charged. Inspect runs before retrying.
5. **Local publication → Request approval** pins an accepted announcement's text,
   revision and digest. **Record approval** records only the native decision;
   inspect and confirm again before **Continue simulation**. Success reads
   **SIMULATED_ANNOUNCEMENT**: nothing sent or published.

Approval covers only the pinned snapshot. A newer accepted version requires its
own approval, even if the text is identical. If one exists, the UI warns:
**A newer version exists; approving publishes this older version**. Both current
and older snapshots require explicit confirmation. Approval never carries forward.

Follow the [Northwind walkthrough](../examples/northwind-prd/README.md) for a source
file example and the [generic Canvas guide](generic-canvas.md) for definition
limits, identities, recovery and exact-snapshot approval. Replay is zero-model
journal verification, not evaluation or proof that a document is good.

Generic definitions use embedded-local execution only: remote Sessions,
Substrate and cloud execution are not supported for Northwind. Suite image
metadata is provenance, not a runnable-image claim.

## Using a hosted model instead

The offline route runs `qwen3:8b` locally through Ollama. Alternatively, supply
an existing HTTPS OpenAI-compatible endpoint and choose a version-pinned model
or deployment. Canvas records the selected name; it cannot establish immutable
provider weights or behavior behind that name. No cloud deployment is required
or qualified here, and generation may incur provider charges.

Create the Git-ignored `model.config.local.json` in the repository root before
selecting hosted mode (the `.invalid` endpoint below is a placeholder):

```json
{
  "profile": "hosted",
  "hosted": {
    "endpoint": "https://model.example.invalid/v1",
    "model": "<version-pinned-model-or-deployment>",
    "keyFile": "~/.config/canvas/hosted.key",
    "auth": "bearer"
  }
}
```

The local file is a **complete replacement**, not a merge with
`model.config.json`. Only when it is absent does Canvas read the shipped
`model.config.json`; if both are absent, it selects offline. Unreadable,
symlinked or nonregular files, malformed JSON and invalid config structure fail
closed: Canvas reports the selected file's error instead of falling back, even
with a profile override. Hosted-field requirements apply when hosted is selected.
The shipped offline `model.config.json` remains tracked; leave it unchanged for
personal hosted settings. Keep API key contents in the separate private key file,
never in either JSON file.

- `endpoint` and `model` are required. The endpoint must be https.
- `keyFile` defaults to `~/.config/canvas/hosted.key`. Put the API key in it and
  `chmod 600` it; Canvas refuses key files other users can read. Keys are never
  read from environment variables.
- `auth` is `api-key` (Azure's header, the default) or `bearer`.

After replacing the placeholders and creating the private key file:

```sh
make dev MODEL_PROFILE=hosted  # checks metadata, then starts Northwind
```

Set a different key path with `hosted.keyFile` in the local JSON. Hosted mode
does not touch Docker or Podman and ignores `MODEL_PORT`. `MODEL_PROFILE=hosted`
by itself cannot supply missing endpoint/model fields; the shipped configuration
selects offline.

### Diagnosing an endpoint without generating

```sh
make check-model                      # local config, then shipped config, then offline
make check-model MODEL_PROFILE=hosted # explicit hosted selection
```

This makes one GET request to the selected provider's metadata route (`/models`
for hosted, `/api/tags` for Ollama). It does not start Sessions, create session
state or generate an answer. Hosted checks use the same private key-file policy;
keys are not discovered in the environment or sent through proxies/redirects.
A successful check confirms metadata-endpoint reachability, **not** inference
support or availability of the selected deployment. Authentication, unsupported
routes, malformed responses and transport failures return bounded diagnostics,
without printing response bodies. `make dev` runs this finite diagnostic via
`sessions-host -check-model` before launching Canvas with embedded Sessions; a
failed check prevents runtime startup. `make check-offline-model` retains the
separate pinned Ollama inventory verification.

The UI marks new assessments using the offline `qwen3:8b` configuration: local
execution can take several minutes, especially on CPU. This is guidance, not a
timing guarantee; historical runs retain their observed model and duration.

Runs retain their recorded model and contract identities. Historical fork ancestry
remains readable, but Canvas does not create new comparison forks. Recovery must
match the recorded execution contract rather than silently switching models;
select a fresh run when you want new work.

## Resetting

Default startup uses `.state/northwind`; it does not adopt legacy Draft Room
state. Keep old state and its history private, and back it up before upgrading.
The football backend, example and reset helper are removed. There is no automatic
reset or conversion to a generic workspace. Historical session profiles and
archives remain supported by the existing compatibility reader; that does not
reactivate old football workspaces or bypass a definition/state-binding refusal.

For a fresh Northwind workspace, stop Northwind, preserve its existing directory,
and select a new state path explicitly:

```sh
go run ./cmd/canvas --definition examples/northwind-prd/canvas.yaml \
  --state .state/northwind-next --model-profile offline
```

Use the same model endpoint/configuration as before (`--model-base` selects a
nondefault Ollama port). A definition change refuses incompatible state rather
than rewriting history.

Default state paths live under `.state/`, which is git-ignored. Custom `--state`
paths must also be private and kept out of version control. State contains
prompts and model output; do not commit or share it.

## Upgrading existing state

Before starting this runtime against existing state, stop Canvas and its host
and back up their complete state directories, including the journal and contract
archive. The current runtime migrates the journal schema on open; in-place
downgrade is not supported. See [runtime compatibility and backup guidance](runtime-upgrade.md).
