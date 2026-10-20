# Native document producers: embedded and remote

Canvas's native profile-4 harness constructs the same journaled model request in
embedded Sessions and over harnesswire. The producer is generic: instructions,
model identity and document scope come from the selected definition's captured
AgentSuite contracts, not a scenario compiled into an image.

The default `copilot-review` remains the deterministic Northwind fixture. Real
providers and remote Sessions connections require explicit options and a separate
private state directory. Neither option supplies a human identity.

## Embedded real provider

Configure the existing [model profiles](local-models.md), then opt in:

```sh
go run ./cmd/copilot-review -model-profile hosted \
  -definition examples/release-review/canvas.yaml \
  -state .state/hosted-document-review
```

`-root` selects the repository/configuration root. `model.config.local.json`
replaces `model.config.json`; hosted settings require an HTTPS OpenAI-compatible
endpoint and a private key file. `-key-file` overrides that file location, never
its contents. No environment credential or offline fallback is used. Do not put
keys in argv, definitions, inventories or Git.

The release-review showcase declares **gpt-5.6-terra** in both released suites;
configure that exact hosted model. Its pins cannot be overridden. The retained
samples are used in fake-provider transport tests here; the showcase's earlier
real-model run does not qualify this new 4096-token path.

For a definition authored for an already configured, pinned local Ollama model
(such as Northwind, not this showcase), select `-model-profile offline`;
`-model-base` may override its HTTP(S) origin. This checks the existing model lock
at startup and before each call. It does not
install or start Ollama. Hosted mode rejects `-model-base` rather than ignoring it.

Local unreleased suites can derive model-specific execution archives using the
existing profile-selection rules. Released suite pins cannot be rewritten: author
and release a compatible suite when its model differs from the selected profile.
Use fresh state when changing the definition/provider; never reset an old budget
or claim an old fixture run was real inference.

Native journaled `max_tokens=16384` stays unchanged for historical request hashes.
The live provider ceiling is **4096**: `max_completion_tokens` for the hosted
reasoning-model path, including reasoning; `max_tokens` for Ollama. Before the
unary Sessions adapter discards provider metadata, the transport requires a single
nonempty completion, no refusal/tool calls and a `stop` finish. Truncation,
content filtering, missing finish, HTTP errors, ambiguous JSON and bodies above
4 MiB fail. Strict recorded schema is required; there is no JSON-object fallback
for native requests. Schema numbers are preserved without float64 conversion.
HTTP replay/retry mechanisms are disabled; failures do not automatically retry.
Legacy profile-1–3 700-token/plain-response behavior remains compatible.

The review panel labels real mode and its **provider invocations this boot**.
This is not a durable HTTP-dispatch or billing ledger. Valid returned content
still passes Canvas's whole-document/schema/scope checks and becomes a proposal,
not an acceptance or action approval. Fake-provider tests establish this boundary,
not model quality, actual billing or live provider compatibility.

## Prepare an immutable remote inventory

Use a normal checkout (`umask 022`); retained suite identities include source file
modes. Prepare from the exact definition/model intended for the Sessions host:

```sh
mkdir -p -m 700 .state
go run ./cmd/canvas-harness prepare \
  -definition examples/release-review/canvas.yaml \
  -out .state/native-producers

go run ./cmd/canvas-harness serve \
  -inventory .state/native-producers/inventory.json \
  -ready-listen 127.0.0.1:8081
```

The output directory must be new. Existing contracts are never overwritten or
reset. Preparation resolves and validates the definition, then writes private
`contracts/<digest>/agent.yaml` files containing the exact `Bundle.Source` bytes
and a closed version-1 `inventory.json`:

```json
{"version":1,"entries":[{"bundle":"contracts/<64-lowercase-hex-digest>/agent.yaml","address":"127.0.0.1:9000"}]}
```

The example digest is explanatory, not a runnable inventory. Preparation emits
real digest paths and one unique port per bundle, starting at `-base-port` (9000
by default). Read the emitted entries; do not assume which role owns the first
port. The descriptor is exactly `bundle-v4-<digest>`, matching embedded profile 4.
The host must register all these exact endpoints/descriptors, not rename them or
substitute an ad-hoc actor. A producer endpoint is **not** a Sessions endpoint.

Serve validates the entire bounded inventory and archives before binding. Unsafe
paths/symlinks, duplicate entries/identities, overlapping ports, invalid archives
or non-native suites refuse startup. Failure and cancellation close listeners.
Serve does no suite fetching and has no model endpoint, key or retry policy: its
model callback is supplied by the Sessions host. `/readyz` is available on the
separate readiness listener after all producer listeners have been prepared.
It is a process-startup probe, not proof of model/host connectivity.

For private pods only, preparation may use explicit `-bind 0.0.0.0` and serve may
use `-ready-listen 0.0.0.0:8081`. These producer and readiness listeners are
unauthenticated; keep them on a private, restricted network. Do not expose them
publicly. The generic non-root image target is:

```sh
docker build -f deploy/Dockerfile --target producer -t canvas-producer:local .
```

Mount the prepared directory read-only with ownership readable by the image's
UID/GID 65532 (or an explicitly selected non-root runtime identity). Do not weaken
private file modes to make a mount work. The image contains the binary, not your
scenario or credentials. Image/cluster lifecycle qualification is separate from
the command's fake-harnesswire tests.

## Separate unauthenticated trusted-loopback connection

For a trusted single-operator port-forward/private development connection:

```sh
go run ./cmd/copilot-review -state .state/trusted-document-review \
  -definition examples/release-review/canvas.yaml \
  -sessions-trusted-loopback 127.0.0.1:18080
```

This explicitly uses plaintext Sessions on **literal `127.0.0.1` and a numeric
nonzero port**. DNS names, wildcard addresses, URL schemes, proxy routing and
implicit downgrades are refused. It is mutually exclusive with `-sessions-config`
and embedded model options. The HTTP engine remains literal-loopback protected by
its existing Host/Origin/cookie boundary; that boundary is not human authentication
or isolation from other trusted local processes.

The engine/panel labels **identity not exercised**, keeps role attribution
unverified, and reports remote model/executor counters as **unknown**, not zero.
It does not use the local archive resolver for remote provenance. Replay
Diagnostics, including announcement verification, are disabled without attempting
a plaintext Diagnostics fallback or displaying a saved local 0/0 claim.

For authentication use the independent [TLS/token-source path](authenticated-sessions.md).
It still requires trusted CA/hostname verification and per-action identity; the
trusted option cannot weaken or fall back from it.

This command delivers the document-producer transport. It does not qualify the
remote simulated-announcement executor, copied-session operator replay, host
multi-endpoint chart wiring, kind lifecycle, a live model or AKS. Those require
separate implementation and measured model/executor evidence. Do not infer them
from readiness, inventory parsing, a proposal or fake-provider tests.
