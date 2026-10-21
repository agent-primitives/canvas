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

## Opt-in simulated announcement transport

**Auth-off/private simulation only. Hosted operation is incomplete until
host-authoritative dispatch bindings are available; this is not the signed-in milestone.** Nothing
is sent or published. The hosted engine/panel still refuses announcement actions;
these commands provide transport infrastructure, not an enabled hosted workflow.

To append the deterministic announcement producer, prepare a new directory with
`-simulated-announcement`:

```sh
go run ./cmd/canvas-harness prepare \
  -definition examples/release-review/canvas.yaml \
  -out .state/native-simulated-producers \
  -simulated-announcement
```

This emits a closed version-2 inventory. Bundle entries have exactly `kind`,
`bundle` and `address`; the announcement entry has exactly `kind` and `address`:

```json
{"version":2,"entries":[{"kind":"bundle","bundle":"contracts/<64-lowercase-hex-digest>/agent.yaml","address":"127.0.0.1:9000"},{"kind":"simulated-announcement","address":"127.0.0.1:9001"}]}
```

The example illustrates the schema, not the actual role count or a runnable
archive. Read the emitted inventory: the single announcement is last, after all
bundles, with a unique port. Its descriptor is exactly
`canvas.simulated-announcement.v1`, version `1`; its approval-required tool is
`canvas.simulated-announcement`. Inventory version 2 does not change those
identities. The entire inventory (at most 32 total entries) is validated before
any listener opens. Serve accepts versions 1 and 2. Without the preparation flag,
version-1 output and existing producer behavior are unchanged.

The announcement harness is model-free. **Producer `serve` never opens an
executor store.** Run the separate executor process on the host side of the
private Unix socket, not in the document-producer process/pod:

```sh
mkdir -m 700 .state/simulated-sockets
go run ./cmd/canvas-harness executor \
  -socket "$(pwd)/.state/simulated-sockets/announcement.sock" \
  -state "$(pwd)/.state/simulated-executor" \
  -auth-mode off -trusted-network=true
```

Both opt-ins are mandatory. Missing/required/other auth modes refuse before
creating state or a socket. The executor cannot infer a remote host's auth mode:
matching that mode is an operator obligation, not authentication. Before a host
assembly enables this profile, its runtime must independently refuse the actual
announcement descriptor/tool in required mode, even with renamed registrations.
That host assembly is separate work; chart validation alone is insufficient.

The socket path must be absolute, clean and at most 103 bytes; its existing
parent directory must be nonsymlink, owned by the process user and mode 0700.
The executor creates a 0600 socket, refuses every existing path, and **never
unlinks it**, including on clean shutdown. Before restart, verify the old writer
is stopped and remove its stale socket explicitly, or provide a new private
socket path. Do not remove a live socket to bypass the store lease. Unsafe
permissions are refused, not repaired. The retained state path must be absolute
and separate from the socket directory; the existing durable executor opens a
new private subtree or its exact compatible store without resetting history.

The existing `producer` image target contains this subcommand. In a future
assembly, run it as a separate nonroot host-pod sidecar, mounting only its prepared
retained executor subtree and the host/executor private socket directory. Do not
mount the host journal/model ledger or inject model credentials/workload tokens.
Keep the ledger in the stopped-writer coordinated recovery set alongside engine
and host state. Unix sockets do not cross pod boundaries. Pod NetworkPolicy is
shared by containers: this placement does not prove per-container no-egress
isolation. This document does not change the existing chart or install procedure.

### Unix protocol and failure semantics

The announcement-only adapter accepts the existing host executor protocol: one
UTF-8 newline-terminated JSON request per connection, at most 1 MiB including the
newline. Exact required fields are `version` (integer 1), `harness` (the exact
announcement descriptor), `session_uid` (nonempty, at most 4096 bytes), `call_id`,
`tool`, `arguments`, and `idempotency_key`. Arguments strictly decode as the
announcement snapshot schema in any key order; call ID and idempotency key must
both equal its operation. Unknown/duplicate/case-folded/null/invalid fields,
wrong routes, oversized or incomplete frames close the connection without
admitting an executor invocation. The adapter supplies fixed approval-required
mediation only for that exact route; it is not an independent approval check.

A response has `version:1`, the same `call_id`, and `is_error`, with optional
`output` or sanitized `error`. Successful output is the unchanged
`SIMULATED_ANNOUNCEMENT` receipt from the shared durable executor. Explicit
matching transport duplicates increase invocation count but return one matching
outcome; changed snapshot content conflicts. No automatic retry occurs.

There are at most 16 active connections and a five-second context/I/O bound per
connection; overload closes the extra connection. That bound may end between
admission and outcome commits. Shutdown cancels peers, drains workers, then
closes the store and releases its one-writer lease. A detected uncertain store
stops the server with an error rather than consuming further approvals.

**A failed/lost socket reply does not prove no receipt.** Native Sessions may
record a terminal `EXECUTOR_ERROR` while the executor ledger already contains a
receipt. Only host-run cancellation leaves an approved native call unanswered;
an ordinary transport error is not automatically a recoverable pending approval.
Operators must reconcile both ledgers before new work; restarting alone does not
repair their disagreement or authorize native retry. Explicit transport duplicate
tests are not reconstruction replay or evidence of exactly-once external effects.

Anyone who can access the socket can fabricate a simulated-receipt request; a
receipt alone never proves human approval. Approval and explicit Continue are
separate in Canvas-mediated execution, but direct Sessions Resume lacks the
additional authoritative Continue/resource/current-generation bindings. The host
must supply those before complete hosted/signed-in execution. Later hosted enablement
must also reject superseded snapshots on decision/Continue and preserve current
revocation/generation/unknown-outcome fences without changing standalone local
older-snapshot semantics. No hosted action route is enabled here. Remote replay
Diagnostics remains disabled, counters remain unknown, and missing evidence is
incomplete—not a zero/zero success.

## Standalone hosted TLS engine

For a separate non-root, headless TLS engine connected to an existing private
Sessions host, see [the hosted engine operator guide](hosted-engine.md). The
`engine` image / `canvas-engine -config FILE` path requires explicit trusted-network
auth-off configuration, prepared local definition/layouts, a private instance-bound
state child and explicit CA/hostname verification. It has no embedded host or
provider credentials. Identity and remote announcement/replay are not exercised;
remote model/executor counters stay unknown. This does not change the local mode
below.

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

The connection command delivers document-producer transport. The separate Unix
simulation tests exercise the typed executor protocol and durable receipts, not
complete hosted wiring, authoritative dispatch, copied-session operator replay,
kind lifecycle, a live model or AKS. Those require separate implementation and
measured model/executor evidence. Do not infer them from readiness, inventory
parsing, a proposal or fake-provider tests.
