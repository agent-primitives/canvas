# Generic local Canvas

## Start explicitly

Canvas starts strict `canvas.local/v1alpha2` definitions with embedded local
Sessions. The default definition is `examples/northwind-prd/canvas.yaml` and the
default state is `.state/northwind`; use `--definition` for another generic
definition and separate state for its history. Empty definitions are refused.
`make dev` prepares the model and selects Northwind explicitly.
First-time readers should start with the [local model setup](local-models.md#quick-start)
and the [Northwind walkthrough](../examples/northwind-prd/README.md).
For new definitions, use the [manual-only authoring starter and offline validation](canvas-authoring.md)
before deciding whether to start a runtime.

```sh
# After setting up the pinned local Ollama endpoint:
make northwind MODEL_PROFILE=offline
# Equivalent with the standard local endpoint:
go run ./cmd/canvas --definition examples/northwind-prd/canvas.yaml \
  --state .state/northwind --model-profile offline
```

This starts the embedded local host, not a separate `sessions-host` process.
The command does not start Ollama. For an already configured hosted endpoint,
use `MODEL_PROFILE=hosted`; see [model configuration](local-models.md#using-a-hosted-model-instead).

For a provider-free run, use [the generated-canvas fixture walkthrough](your-first-review-canvas.md).
`--fixture` still requires an explicit `--definition` and `--state`; the default
Northwind model-backed path is not an implicit fixture. Real and fixture state
remain separate, and opposite-mode state is refused without changing history.

Normal model configuration and key-file rules still apply. Starting a configured
host does not generate an answer. Seeded feedback is context, not a new-file
event. Explicit requests and new or changed declared input files can invoke the
model; hosted inference can incur charges.

Use separate state for each definition. A changed definition digest or document
policy refuses existing state instead of reinterpreting its history. Stop the
app and select a new state directory; do not delete historical state to disguise
a mismatch. A model-selection override creates a different archived execution
identity while preserving the authored suite/member identity.

Generic Canvas uses its embedded local runtime. `--sessions-address` refuses
before provider/client setup, with either the default or an explicit definition.
The standalone `sessions-host` defaults to Northwind and separate
`.state/northwind-host` state. It is host-only support; generic Canvas does not
connect to it through `--sessions-address`. Substrate is refused, including when
no definition flag is supplied. Native remote factories and the actor bootstrap
CLI also refuse generic execution. Delivering native archives to Substrate
templates is a separate follow-up. Historical request profiles 1–3 remain for
recorded-session compatibility, not as a football startup path.
No cloud deployment or runnable-image qualification is claimed here.

Existing legacy ledgers, workspaces or sessions archives are refused before
runtime/cache writes; mixed legacy and native archives are refused too. Existing
generic bindings are checked again when state opens. Keep old state intact and
select a separate path rather than resetting it to hide a mismatch.

## Northwind walkthrough

1. Open the local URL. Navigation lists the declared writer and independent
   reviewer, their single-agent triggers, documents and fictional people.
   Personas record attribution only: they are not accounts or authorization.
2. Select **New… → New agent run**, choose **PRD writer**, and request a fictional
   pilot proposal. The run records its accepted base, scoped inputs, model,
   execution identity and pinned suite identity. It cannot accept its own work.
3. Review every affected replacement using **Review affected documents**. Accept
   applies the whole validated set together; Reject leaves accepted documents
   unchanged. An invalid or out-of-scope member rejects the complete model result.
   A changed read input, including a new file in a read collection, makes a
   pending proposal stale; regenerate explicitly against the new evidence.
4. Accept the writer draft, then explicitly request **Independent reviewer**.
   That separate run reads the accepted PRD at its recorded base, not an
   unaccepted writer result. There is no hidden multi-member orchestration.
5. To add feedback, use **New… → Add source file**, path `feedback/new.json`, and
   content such as:

   ```json
   {"team":"Northwind","feedback":"Fictional follow-up: rollback and opt-in criteria remain unresolved. No duration improvement has been measured."}
   ```

   Saving validates the file first; the file watcher checkpoints valid bytes
   before queuing one durable run per trigger/path/content identity. You can also
   drop a bounded regular file into the workspace's declared input directory
   (the workspace path is returned by `/api/state`). Existing input bytes are not
   rewound when another document proposal is accepted. If publication succeeds
   but its durability sync fails, the error explicitly warns that the watcher
   may still start a chargeable run; inspect current state before retrying.
6. Use **Ask agent why** on a supported recorded run for a read-only question on
   that session. It cannot propose, accept, or send anything. A new question
   invalidates old Replay proof until explicitly reverified.
7. Open **Evidence** for exact identities, source revisions, chronology, and
   explicit Replay/reconstruction verification. Zero-call Replay confirms journal
   integrity/reconstruction, not whether the PRD or authored suite is good.

## Approve an exact announcement snapshot

The writer can propose PRD and announcement drafts. Accepting a proposal only
reviews document changes; it never publishes an announcement. The optional
`actions.announce` declaration names a separate, zero-model native Sessions
harness. It does not add tools to the writer, reviewer, or profiles 1–4.

1. Request an announcement draft from the writer and accept the proposal after
   reviewing all its affected documents.
2. In **Local publication**, select the declared action and an accepted proposal
   that changes its announcement document. Select **Request approval**. A seed
   alone or an unaccepted proposal cannot create a gate.
3. Inspect the pinned text, accepted revision and digest. Check the explicit
   confirmation, then **Record approval** or **Record denial**. This records a
   native journal decision only; it does not continue the simulation.
4. Inspect again and check the newly cleared confirmation. **Continue simulation**
   explicitly resumes the native execution. Only a successful, bound native
   tool receipt displays **SIMULATED_ANNOUNCEMENT: recorded locally; nothing sent
   or published**. A denial or executor error is never a successful receipt.

Approval authorizes exactly the immutable bytes at the selected proposal's
accepted revision, not the latest workspace document. If a newer accepted
announcement revision exists, the UI says **A newer version exists; approving
publishes this older version** and requires the same explicit confirmation.
Continuing that approved operation still uses the older bytes. A newer revision
requires its own request and approval, even if its text matches an older version.
Approval never carries forward. Unrelated feedback revisions are not newer
announcement versions. Changed inspection evidence clears confirmation; stale
confirmation refuses rather than substituting the latest document.

This is embedded-local-only. Factory/standalone hosts do not serve this native
action. No notification credentials, connectors, external sends or publication
side effects exist. Personas remain attribution, not authentication or authority.
A native receipt proves this local simulation, not evaluation of a suite or model.
The added action/scope changes the example definition identity: preserve older
state and use a fresh separate Northwind state directory.

Interrupted dispatch becomes uncertain and is never repeated by startup or
polling. **Recover approval request** is explicit, uses a guarded native Resume,
and requires reinspection and new confirmation afterward. At the saved-session,
before-first-Exec cut, recovery can issue only the original exact input with its
original journal cursor if native proof shows no execution. If a session UID was
never saved, **Abandon local request** preserves its audit record and allows a new
explicit request; it does not claim to cancel any orphan native session. Once a
decision exists, use Continue, not Recover. A crash after an invocation start but
before its input is recorded is marked **incomplete**: it cannot be resumed from
missing inputs. With fresh matching confirmation, local Abandon is also available
when the native session cannot be read, or verified history has no decision and
no pending approval (including an externally cancelled session). Native work may
already have progressed: Abandon neither cancels nor undoes it. It preserves the
audit and native journal, freeing the action/proposal pair only for a new explicit
request with its own approval, never repair or automatic resend. A late orphan
cannot be approved or continued through its abandoned Canvas operation. Native journal requests, decisions
and receipts are authoritative; the Canvas ledger stores snapshots and dispatch
intents only.

## Identity and evaluation boundaries

`github.com/agent-primitives/suite v0.7.0` validates the captured directory and
matching local OCI layout. Both views must have equal full-inventory content
identity; the layout must match the configured artifact pin. Canvas delegates graph validation and evaluation verification to the shared library.
It bounds transport and decompression before validation and does not parse kmx private receipts.

Evidence keeps these identities separate:

- **Artifact digest**: pinned OCI AgentSuite artifact (`suite.digest`).
- **Content digest**: full retained inventory (`suite.contentDigest`).
- **Graph digest**: canonical `agentsuite.json` (`suite.graphDigest`).
- **Member digest**: the selected canonical member descriptor, independent of its
  local Canvas alias (`suite.member`, `suite.memberDigest`).
- **Compositions**: the full sorted selected-member composition list, including
  platform/build-profile identity. Neither platform image is executed by Canvas.
- **Execution archive digest**: exact Canvas archive bytes, including selected
  model; this is the immutable harness identity, not the OCI pin.

Legacy definitions report **evaluation: not verified**. Released definitions can
report **evaluation consistent; unsigned** only after shared `VerifyEvaluation`
checks the exact retained OCI layout against independent expectations and detached
exported evidence. Format validation, filenames, saved UI labels and Replay cannot
upgrade that status. Missing/mismatched evidence stays **not verified**, with a
reason. This is release-level consistency, never proof that this Canvas execution
used the tested runtime, or authentication/attestation of evaluator observations.

The example's required image metadata uses synthetic `example.invalid` references.
It is provenance-only, never pulled, and is not evidence of image executability.

## Released references and headless JSON

The legacy `suite: {directory, artifact, digest, member}` shape is unchanged.
A released alias instead uses:

```yaml
suite:
  ref: registry.example.invalid/team/writer@sha256:<manifest-digest>
  layout: layouts/writer
  member: writer
  platform: linux/amd64
evaluation:
  expected: evidence/writer.expected.json
  evidence: evidence/writer.export.json
```

`layout` optionally supplies exact S offline; `ref: sha256:<digest>` requires an
offline layout or previously validated cache entry. Without a layout, a pinned
registry reference fetches anonymously over HTTPS with redirects refused. Registry
authentication is not configured: bearer-token challenges and redirected blob
CDNs refuse; supply an offline layout for those registries. Transport tests use
a local TLS registry fixture, not a qualified live registry deployment.
Cache entries live under `<state>/suite-cache`; existing state/cache roots must
be owned private directories, never symlinks or permission-repaired foreign paths.
The resolver refuses symlinked directory aliases anywhere in its cache path;
use a canonical real path, including on systems with symlinked temporary roots. Entries are bounded, validated and atomically
published by S. Corrupt entries refuse rather than silently falling back to network.

`member` and `platform` are mandatory. Omitting `runtimeProfile` explicitly selects
an unprofiled composition, not the first profile. A unique validated tuple selects
its exact composition and referenced build profile; ambiguity refuses. Declared
public profiles incompatible with the local harness refuse before registration.
Model changes, selected tools/invocations/policies the harness cannot enforce,
missing platform image associations and mixed legacy/release fields also refuse.
S is never repackaged. Module/schema versions and all digest domains stay separate.

Independent expectation and evidence files are bounded local regular files with
portable paths. Their retained bytes participate in the execution archive and
Canvas definition identity. Historical panels derive from validated matching
archive bytes, not a persisted status flag or newer alias metadata. Changed
definitions require separate state; no old archive migration occurs.

The browser keeps `/api/*`. The same guarded listener also serves headless JSON
at `/engine/api/*`, without assets, legacy scenario/role actions or room chat.
The Copilot adapter maps its existing allowlisted URLs to that surface when the
engine advertises it. Host/Origin/cookie, boot and adapter-capability checks stay
in place; this remains an unauthenticated local prototype.

Headless routes cover state, exact documents, manual run, document review,
reconstruction, existing announcement request/decision/Continue/verification, and
GET-only `/engine/api/forks?run=<id>`. Fork readback separates recorded parent
session/checkpoint, child status and document base; absent branch/session evidence
is unavailable, never invented. It cannot create forks or inherit approval.

## Definition and document limits

`examples/northwind-prd/canvas.yaml` shows documents, schema paths, agents,
read/propose scopes, suite bindings, seeds, triggers, person labels and optional
actions. An action has a name, `kind: simulated-announcement` and an exact output
document ID; inputs, globs and unknown kinds refuse. An agent
proposes document IDs, never arbitrary paths. Only whole-document `replace`
changes are supported. A separate laboratory-shaped fixture exercises another
project, agent alias and Markdown target through the same execution/review code.

Paths are portable, relative and nonprivate; symlinks and special files refuse.
Only basename `*.suffix` input globs are supported, not prefix globs or `**`.
Every glob is read-only input; input writes cannot bypass output review. Exact
inputs and outputs cannot overlap, including case aliases and file/directory
conflicts. Person IDs retain `GM`, `coach`, and `owner`; names/titles are configurable.

JSON schemas compile once and validate seeds, input files and proposed contents.
Embedded fragment references work; relative external, file, HTTP and HTTPS
references reject without file/network loading. Each replacement is at most
64 KiB; one result is at most 32 replacements and 256 KiB aggregate. The serialized
model input snapshot is at most 256 KiB, including accumulated accepted input
files and declared schemas. This is a context limit, not automatic truncation or
an evaluation gate. An input that exceeds the reader context is checkpointed but
marked refused without a model call or endless retries for the same content key.
Replace large inputs at their existing paths with shorter valid content (through
the source form or by editing the workspace files); those replacements are
checkpointed normally and retain prior Git history. Replacements are new input
events: the one that brings context under the bound may itself trigger a
chargeable run. Inspect recorded runs before explicitly requesting another.
For a different document scope, use separate state;
never delete historical evidence to hide the limit.

Input discovery is bounded to 128 entries
per declared directory per scan. Source suite capture is bounded to 128 files,
1 MiB/file and 4 MiB raw combined across directory/layout, with 8 MiB aggregate
gzip expansion; the versioned JSON execution archive is at most 8 MiB.

Profile 4 is a fixed, tool-free one-model-operation harness. Its recorded output
format does not read mutable definition files. Canvas performs current declared
schema/scope validation before publishing a proposal. Unknown suite features,
multiple members, tools/invocations, environment/secret requirements and unsupported
model protocols refuse instead of being silently converted.

## Failure and recovery

Queued/running work interrupted by restart becomes uncertain; it is not resent
by startup, polling or input deduplication. **Try again on latest** starts a fresh
attempt on current accepted documents, leaving the original evidence intact.
**Resume** remains separate: only positively identified modern pending model work
from an exact known tool-free harness is eligible, with fresh inspection and the
warning **“Re-sends the last model request; it may be charged again.”** Confirmation
creates a durable once-only action intent. Stale, ambiguous, malformed, tool or
approval recovery refuses. Uncertain delivery never causes an automatic resend.
