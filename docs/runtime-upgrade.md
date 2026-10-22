# Sessions runtime compatibility

Canvas builds against the canonical `github.com/agent-primitives/sessions`
v0.3.0 release, including native approval, authentication interceptors and guarded
Resume:

- Core: `v0.3.0` (`bcbd4d9dcb352a10444119c2e6f75b4715dc68e0`).
- Substrate integration: `v0.3.0`, tagged `integrations/substrate/v0.3.0`
  (`d74eec3477722c2e6a8580815f86d5b6eaa27090`).
- Resolved Substrate: `v0.5.0`, required by the integration.
- Go floor: 1.27.0 (required by the integration); selected toolchain: 1.27.3.

These are ordinary Go module requirements, without fork or workspace replacements. Use
`GOWORK=off make build` and `GOWORK=off make test` to verify the shipped modules.
The Docker build-stage digest is the registry-verified multi-platform index for
`golang:1.27.3-alpine`; the app runtime image remains separately pinned.

## Journal upgrade and backup

Stop the host, Canvas and every other process sharing the journal before taking
a consistent backup of their entire state directories, including the journal and
immutable contract archive. Preserve that backup separately before starting the
new binary, and upgrade all writers before restarting any of them. Do not copy
only a live SQLite main file: uncheckpointed changes may be in its WAL.

Journals written by the fork pin `ce018b2daa0f` or the previous upstream pin
`f18a2cb1454d` use SQLite schema 5 and migrate to schema 6 on open. Schema 6 adds
per-session model-policy state; existing sessions remain legacy. It does not
rewrite recorded event bytes or hashes. Upstream Sessions' controller
verifies the journal hash chain before Exec, Replay and Resume and refuses a
broken chain. The Exec and Resume RPCs surface that failure as gRPC `DataLoss`.
The read-only Replay RPC still streams stored records without checking them;
Canvas checks replay hashes itself, and its Verify diagnostic rejects a broken
chain with `FailedPrecondition`. These checks do not repair the journal or
replace a stopped-state backup.

Opening a journal written by the earlier Canvas pin, `9a3d755c1072`, migrates its
SQLite schema from version 1 to version 6, including the harness registry,
approval-reporting, authentication and model-policy schemas. Migration alone does
not authorize or continue an approval; the separate local action requires explicit
inspection, decision and guarded continuation.
Migration happens on open, not only on the first new turn. An old binary refuses
the newer schema. In-place downgrade is not supported or qualified; restoring the
stopped-state backup is a separate operator action and discards later writes.

The upgrade regression uses `internal/sessionhost/testdata/9a3d755-completed.db`,
a synthetic fixture generated through the old Canvas host at `c996f60` and its
normal `9a3d755c1072` module dependencies, with no workspace replacements. Its
private temporary host made two scripted model calls, one completed parent turn
and one completed fork-child turn. It contains no operator journal or real model
output. The fixture bundle has instructions `old pin fixture instructions`, model
`fixture-model`, and output `{"strategy":"old-pin-fictional"}`. Its parent prefix
is four records; the child retains that prefix, its fork marker, and its own turn.
The regression copies the database before opening it, checks schema 1 → 6 and
byte-identical existing event rows/hashes/fences, preserves lineage, and verifies
completed replay and explicit Resume without a provider call. This is bounded
upgrade evidence, not a general journal-repair or rollback qualification.

The fork-pin regression uses `internal/sessionhost/testdata/ce018b2-native.db`,
written through Canvas at `58be923` with its normal `ce018b2daa0f` fork dependencies.
It contains one completed native question turn and one unanswered native model
intent in separate sessions, using only scripted fictional provider results. The
test records the generation procedure, checks schema 5 → 6 and unchanged historical
event rows/hashes/fences, and verifies both chains. Completed reconstruction and
Resume make zero provider calls; startup and read-only verification never reissue
the unanswered intent. Explicitly resuming that intent makes exactly one provider
call; subsequent Resume and verification make no extra calls.

## Substrate lifecycle and transport

Substrate v0.5.0 is **not an in-place upgrade** from the previous `96dffb287919`
pin. Reinstall Substrate with fresh state and recreate templates and actors using
the matching v0.5.0 API and v0.3.0 adapter. Old actors and their held checkpoints
do not carry over; restoring a Sessions journal alone does not migrate them.
Rollback likewise requires a matching older Substrate installation and adapter,
not a downgrade over v0.5.0 state. These are compatibility warnings, not a
supported Canvas cluster-deployment procedure. See the upstream
[Sessions v0.3.0 compatibility notes](https://github.com/agent-primitives/sessions/blob/v0.3.0/CHANGELOG.md#substrate-v050).

The API reports a paused actor as `SUSPENDED` with a node-local newest snapshot
and a pausing actor as `SUSPENDING`, rather than `PAUSED`/`PAUSING`. The adapter
refuses a local-only newest snapshot until Suspend makes it durable; a pending
upload is `UNAVAILABLE`, and a transitioning actor is `ABORTED`. Native Restore
also refuses a held checkpoint that differs from the journal's reference with
`FAILED_PRECONDITION`: inspect the actor and Suspend the stateless session again
to record its current durable checkpoint before Resume. It does not fall back to
an older completed snapshot. Suspend on v0.5.0 returns only after upload completes.
Memory-tier sessions have stricter loss refusals; Canvas's factories admit only
stateless replay descriptors.

The default backend still releases and deletes an actor on suspension. Current
placement calls only `Snapshot`, so the Canvas default wrapper invokes native
`Stop` exactly once before returning a journal-identity checkpoint. A failed
release/delete returns an error and does not publish a successful checkpoint;
partial-delete recovery attaches to the same suspended actor by session UID.

The retained backend instead calls native `Snapshot` once, releases the worker,
and retains the actor/checkpoint without calling `Stop`. Explicit teardown and
failed-fork cleanup remain destructive. Both policies expose stateless journal
identity references to Sessions, not native memory-snapshot references. Neither
promises snapshot-free activation: Substrate selects the actor's own checkpoint,
a template snapshot, or a fresh start. There is no per-call Boot flag. Canvas's
identity-only Restore goes through native Create, not native Restore's exact
held-reference comparison; native checkpoint readiness checks still apply.

Substrate v0.5.0 also requires a pod namespace calling ate-api-server to carry
`ate.dev/api-access=true` under its default NetworkPolicy. This does not enable
Sessions authentication or change Canvas's existing transport configuration.

ActorTemplates are now atespace-scoped. The existing `-template-namespace` setting
is passed as the template's **atespace**, not a Kubernetes namespace. Harness
incarnations address the atenet router with `ate-target-actor` metadata, not a
worker/pod IP. The adapter constructor and placement dialer follow those current
upstream interfaces.

## Explicit Resume semantics

An explicit runtime Resume of a modern unanswered model intent reissues the
recorded model request; it may be charged again. Another explicit Resume after a
provider failure can reissue it again. Restart and read-only verification do not
invoke the provider; a recorded completion is replayed without another call.
Provider error details remain sanitized. For legacy turns, upstream's OpenAI
adapter now rejects responses including a nonempty refusal string or a
`tool_calls` finish reason with no tool calls, returning a sanitized provider
failure instead of the fork's empty success. Native profile-4 completions already
had Canvas's stricter finish/refusal checks. These responses do not establish a
completed turn; an explicit Resume may reissue the unanswered request as described
above. Producer-facing confirmation and tool refusal are separate Canvas recovery
responsibilities, not guarantees conferred by the runtime API itself.

## Qualification boundary

Cluster deployment is not a supported setup path. Use the local workflow in
the [local model setup](local-models.md#quick-start).

Local fake-Control, scripted-provider and SQLite tests do not qualify cloud
images, a live actor router, snapshot selection, or live cluster deployment.
Approval adoption is qualified only for the separate zero-model embedded-local
simulation in [generic-canvas.md](generic-canvas.md#approve-an-exact-announcement-snapshot).
Profiles 1–4 remain unchanged. Native approval/tool-bearing sessions are never
eligible for the existing model-resend Resume control. Both SDK module paths and
Go imports are canonical; protobuf wire package identities stay `agentsessions.v1`.
The historical SQLite fixture bytes and prior model request hashes remain test
anchors, not regenerated or relabeled evidence.
