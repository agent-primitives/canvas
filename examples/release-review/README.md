# Fictional envctl 0.9 release review

Two real agents, one configured hosted model, accepted-document collaboration,
and a separately approved local simulation. Start with
[tutorial 2](../../docs/review-release-notes-with-two-agents.md).

The launch brief promises a seamless upgrade, but the frozen PR/test/policy
excerpts describe a new default lifetime that can interrupt overnight pipelines.
The writer proposes `notes.json` and `announcement.md`; the compatibility reviewer
reads both accepted documents and proposes only `review.json`. Critique is advice,
not a sign-off gate. The local reviewer accepts/rejects each complete proposal.
No automatic handoff, quorum, authenticated identity or external publication.

`inputs.json` is protected by both agents' proposal scopes, not a compiled
read-only input policy. Both triggers are manual. JSON schemas reject extras and
bound fields/arrays; valid initial documents explicitly say not drafted/reviewed.
Announcement Markdown is bounded by the engine's 65536-byte UTF-8 limit.

## Run and check

Configure an endpoint serving exactly **gpt-5.6-terra**, as declared by both suites,
using the private hosted model configuration in
[local-models.md](../../docs/local-models.md#using-a-hosted-model-instead).
The offline default does not match; released suite models cannot be overridden.
From the repository root:

```sh
go run ./cmd/canvas validate examples/release-review/canvas.yaml --json
make check-model MODEL_PROFILE=hosted
go run ./cmd/canvas --definition examples/release-review/canvas.yaml \
  --state .state/release-review-new --model-profile hosted --listen 127.0.0.1:0
```

Use a fresh private state path. Only deliberate agent requests invoke inference.
For restart, preserve the same state and exact package bytes; do not use the
legacy reset command. Stop before changing a running definition or suite.

Grounding beyond JSON shape is checked with
[example-local cases and a semantic rubric](evals/README.md), not a new engine
truth gate. Run `go test ./examples/release-review` for citation-existence checks
over retained real outputs and negative cases. Valid IDs alone do not establish
that a claim is supported. These evals are **not suite evidence**: the correct
Canvas label remains **evaluation: not verified**.

## Suites and reproduction

The `suites/` directories are complete, tool-free AgentSuites; `layouts/` contains
complete local OCI layouts. Both use schema `1.0.0-draft`, `linux/amd64` selection,
plus a `linux/arm64` composition in each suite. They reuse the existing
`canvas-provenance` build profile and empty tool-provider catalog. The
`example.invalid/canvas/not-executed@sha256:…` runtime/harness references are
provenance placeholders, **not runnable-image or execution qualification**.
Canvas executes its existing local suite harness, not either referenced image.

| Member | Exact OCI artifact digest |
| --- | --- |
| writer | `sha256:661a3b10f3f09e8fb7f2dc71a0df4694200222451cf39da27a3dbde7f38b5c1f` |
| reviewer | `sha256:60c3d5d677bf60ffe470a2792df94022aa51fe19e8446cf953f1576f32dbd15b` |

Produced with **kmx v0.8.1** (`go install` build), using the existing suite tooling,
not hand-assembled OCI blobs, registry publishing, evaluation or Lift. Executable
SHA-256: `cdd97c41abc86aff267372b00a08360f5d7f1ead335c949e3cb04e09bf072348`.
With a compatible kmx version, reproduce into two **unused local destinations**:

```sh
kmx suite validate examples/release-review/suites/writer
kmx suite validate examples/release-review/suites/reviewer
kmx suite push examples/release-review/suites/writer \
  --to-layout /path/to/new-writer-layout agentsuites/release-review-writer:v1
kmx suite push examples/release-review/suites/reviewer \
  --to-layout /path/to/new-reviewer-layout agentsuites/release-review-reviewer:v1
```

Compare returned digests with the table. Retain regular source file modes as
0644; the content layer includes file modes. JSON child manifests are stored in
canonical form and descriptors bind their exact bytes. Changing instructions,
model or manifests requires regenerating all affected descriptors and repacking,
not copying an old digest. No registry access is needed for these commands.

## Approval is separate from documents

`simulated-announcement` pins an accepted announcement proposal's exact text,
revision and digest. Request approval, inspect, decide, inspect/confirm again,
then explicitly Continue. Approval alone executes nothing. Denial remains denied;
a new reviewed proposal needs a new operation, even with identical text. Only one
`SIMULATED_ANNOUNCEMENT` journal receipt results from a successful continuation:
no send, file export, connector or external effect.

The recorded local proof used four real calls: writer, reviewer, concise revision,
then an explicit fresh proposal after denial (identical document bytes). The
reviewer found no discrepancies; none were manufactured. Local decisions were
made by an authorised assistant labelled **local, unauthenticated**. Pending
approval survived a local process restart, approval alone produced no receipt,
Continue produced one receipt, and replay of all four model runs and the action
reported **0 model / 0 tool-executor calls**. Retained fictional output samples
are in `evals/recorded/`; they are not replay journals or portable proof receipts.
The owner still performs an independent usability pass on the finished tutorial.

This local run does not qualify AKS durability, authenticated membership/roles,
shared-host approval permissions or cloud execution.
