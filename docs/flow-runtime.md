# Go workflow runtime (local qualification)

The optional `OpenFlowRuntime` constructor runs compiled v1alpha3 workflows
through the existing native document harness. `OpenDefined` and command-line
loaders still refuse implicit flow activation. The local panel and guarded HTTP
routes are documented in `copilot-review.md`; no MCP decision tools are exposed.
This is not hosted service-credential, grant, sign-in or deployment qualification.

## Scoped document edit proposals

Local v1alpha3 definitions may bind a non-input document to an existing agent
with `editAgent`; that agent must already read and propose the document. Editing
never creates agents or changes accepted bytes. The panel asks that agent for a
separate, fresh native attempt and one whole-document replacement proposal.
Human acceptance remains a separate whole-proposal decision. This slice is
**document-only**: every nonempty `FlowRun` is refused, including unknown or
completed run IDs. An empty association also refuses while any nonterminal flow
exists; failed/uncertain work is not silently treated as complete.

An omitted selector means the whole document. Explicit JSON Pointers must exist
at the inspected revision; malformed, missing or ambiguous selectors refuse
without widening. JSON outside the selected subtree retains identical numeric
lexemes (including `1e0` versus `1`), while whitespace and key order may differ.
Schema-backed edit replacements additionally refuse lossy number conversion;
seed and ordinary/manual validation are unchanged. Markdown section selection
supports exact ATX heading paths and a positive occurrence outside fenced code;
unsupported/ambiguous constructs such as Setext headings refuse. Bytes outside
the frozen section remain identical. This is not general CommonMark support.

One durable edit slot freezes the revision, document digest, scope and request
key. Reads and restart never retry work. Unknown outcomes retain the slot until
explicit exact-request reconciliation or abandonment. A review intent proven
uncommitted by the local Git proposal and complete history restores a pending
proposal for **fresh human review**, never replays the decision. Abandoned output
cannot be accepted through generic review. Other reviews, input ingestion,
manual/flow admission, action decisions and Continue are fenced while the edit
slot is held. Accepting an edit invalidates old-base proposals without reopening,
advancing or rerunning a flow.

A document-only edit refuses admission while the same document has an unsettled
manual action, before spending an edit attempt; it never retires that manual
handle as a side effect. Concluded and unrelated operations retain their records
and receipts. Local abandonment is not proof of native cancellation or settlement;
Native Cancel remains unsupported at the qualified Sessions pin. No synthetic
cancel or automatic cleanup shortcut is provided.

The local HTTP surface is `GET /api/edits`, `/api/edit`, `/api/edit/context` and
`POST /api/edit/request`, `/reconcile`, `/abandon` (all POST
suffixes under `/api/edit`). Review still uses `/api/review`. Requests bind the
compiled document ID, full base revision/digest, optional selector, instruction,
request key and local role. Limits are 2,000 instruction bytes, 65,536 document
bytes, 512 pointer bytes, 16 heading components of 128 bytes, and 128 retained
edit requests per instance. These local routes refuse authenticated/remote
Sessions modes; local labels are unverified attribution, not authorization or
OS containment. No live-model, hosted identity or real-action qualification is
implied by the deterministic fixture.

Durable request/native-attempt binding and Git publication/review intents are
required for standalone lost-reply and restart safety. Reopening may retain an
uncertain record; it never retries a producer or applies a new publication or
human decision. Ordinary explicit Reconcile inspects the exact owned native
and Git evidence rather than guessing success or retrying a producer. An abandoned
document-only edit may also be explicitly reconciled read-back-only: exact
service-owned native execution, input, bundle and stable hash-chain END evidence
sets optional `nativeSettled` and clears only its admission bookkeeping. The edit
and output remain abandoned; no proposal, acceptance, dispatch or Resume occurs.
Missing UID or unknown/incomplete evidence retains the native admission fence.
Nothing automatically opens a repair path or publishes abandoned output.

Flow-associated editing, Resume/Rerun controls and routes, shared-budget
accounting, restart checkpoints and edit-aware aggregate replay remain future
work. The existing flow recovery/replay described below is unchanged; it does
not claim edit coverage.

Maintenance responses distinguish a proven unreserved refusal (`EDIT_REFUSED`)
from a recorded/unknown command. The panel may discard only the former; otherwise
it retains the exact route, key and payload for explicit continuation.

## Host configuration

Pass the same dependencies as `OpenDefined`, plus `FlowRuntimeOptions`:

- `MaxAgentRuns`: a positive operator ceiling. Each Start supplies a positive
  budget no greater than this ceiling or the authored flow limit.
- `OwnerAuthorizer`: optional external `ReviewAuthorizer` for owner maintenance.
  It receives the verified principal, role `owner`, action `cancel`, `reconcile`
  or `advance`, current
  proposal and compiled document scope. An owner persona alone grants nothing
  in authenticated mode.

Configure authentication and the separate `ExecutionService` before serving,
following [the execution/human authority contract](authenticated-sessions.md).
`NewFlowReviewerPolicy` grants a logical gate role to explicit verified identities;
YAML role declarations do not grant membership. The service never becomes a human
reviewer. The unauthenticated local mode uses persona labels, not verified people.

## Run and review

`StartFlow(ctx, FlowStart)` freezes the flow ID, exact validated input JSON,
requesting identity/role, starting accepted revision and selected budget.
A repeated request key with the same payload returns the existing run; changed
bytes or identity conflict. `GetFlow` returns detached snapshots.

Inputs are schema-validated objects bounded to 64 KiB, without default insertion.
The native documents envelope is unchanged: its existing string `request`
contains structured inputs, required-output guidance and feedback. Final input
is bounded to 256 KiB and the encoded request string to 128 KiB; expansion is
refused, never truncated. Manual requests
retain their independent 2,000-byte limit. Status summaries do not echo the
whole structured request.

Each service-owned attempt, live generation and consumed budget are journaled
before dispatch. Generation/supersession records stay in Canvas's private ledger;
Git proposals retain their existing immutable attempt binding. Retried or revoked
generations cannot publish an eligible proposal or restore a consumed gate.
A gate requires every declared output in one valid proposal; additional allowed
replacements remain part of the same whole-proposal review. `ReviewContext`
enforces the exact current proposal, base and compiled gate role in **both**
local and authenticated modes. Authenticated decisions additionally require the
external document authorizer for all changed paths.

Accept advances to the next step using accepted bytes only. Reject ends the
flow without undoing history. `RequestFlowChanges` rejects with bounded attributed
feedback and reserves a counted retry of the same step. `RetryFlow` supports an
explicit retry after completed output validation fails; uncertain native work
cannot be retried. Retry and feedback keys are idempotent. No budget extends
itself.

One active flow owns the instance, including paused, uncertain and action-waiting
states. Competing manual runs, input writes/scans, unrelated document decisions
and public action creation are refused. Refused watch events are not marked
processed and can be scanned later. Generic question, regenerate and resume
paths cannot bypass a flow's attempt binding or budget.

## Cancellation and actions

`CancelFlow` permits the original requester or an explicitly authorized canvas
owner. A quiescent gate or known undispatched/completed-attempt pause can cancel
immediately. For running or uncertain document work, it persists cancellation and
revocation separately before best-effort native Cancel. Native Cancel is
**Unimplemented at the qualified pin**: the call reports that refusal and the
flow stays fenced pending explicit reconciliation of its eventual native outcome.
`ErrFlowCancelPending` distinguishes a saved fence without a native cancel
attempt from a no-write refusal. Repeating a pending cancel preserves its first
actor and revocation. The worker rechecks cancellation before both Create and
Exec intent, suppressing work not yet reserved. Restart cannot erase the fence;
late output cannot become reviewable. Local
personas remain unverified. Accepted history is never rolled back.

Parked and approved-but-not-executed action cancellation remains refused until
native cancellation is qualified. A refused action cancellation writes no fence.
Continue reserves its generation-bound effect intent before the human Resume;
unknown reservation, pending or uncertain dispatch blocks further mutation.
A native authentication/grant refusal precedes mutation and releases only that
call's reservation, permitting a fresh explicitly authorized human Continue.
Both Continue and Recover enforce generation and tombstone eligibility. Native
receipt/denial evidence settles an intent; failed cleanup does not clear it.
These are local engine fences, not qualification of hosted generic Resume grants.

A final action uses the accepted action-document proposal bound to this flow,
even when that document was gated earlier than the last agent step. It parks
under the engine service identity. Human approval records only; a separate
human Continue produces the simulated receipt. Denial ends the flow without an
automatic cleanup Resume. A human may explicitly Continue a known denial to
conclude the native session, even after a newer document revision; this produces
no receipt and does not re-execute the action. Recover is not a cleanup path.
Until that session concludes, a new flow is refused. Public action creation
cannot adopt a flow proposal.

## Explicit recovery and replay

Ambiguous Create/Exec, Git publication or review bookkeeping retains the active
slot. Opening state never dispatches. Nonterminal flows reopen uncertain (or
cancelling with a retained cancel intent), even if a proposal is visible.

`InspectFlowReview` reports typed review evidence without changing the Canvas
ledger or accepted ref. The store may reconcile its own private review ledger.
Local acceptance exposes only an unverified role; a local rejection has no public
actor evidence and remains uncertain. Saved intent alone is not a decision.

`ReconcileFlow(ctx, FlowMaintenance)` is authorized for the original requester
or explicitly authorized canvas owner. Service-owned reads join the exact native
execution/input/head, generation, whole proposal and reachable Git decision.
It may restore a pending gate or record a committed review exactly once, but
never Create, Exec, Approve or Resume. Proven settlement updates the attempt and
flow in one save. Durable action phases distinguish parking/approval from Continue:
matching native parked/approved evidence can restore a usable stage, while unknown
Continue reservations and ambiguous phase-less legacy operations remain fenced. Missing UID, incomplete native evidence,
legacy zero-generation state, stale heads and unattributed local rejections
remain fenced. Missing proposals are not regenerated by reconciliation.

A recovered acceptance waits in `ready`. `AdvanceFlow(ctx, FlowAdvance)` separately
authorizes the original requester or canvas owner, checks the accepted head and
reserves one counted next attempt under an idempotent request key. `TriggeredBy`
records whoever advances, while `RequestedBy` stays the original requester and
`TriggeringReview` retains the accepted proposal. An advance key is completed only
alongside the next attempt or action intent; an unreserved failed key returns a
conflict rather than silently claiming successful dispatch. Neither maintenance operation
creates human decision credentials. Actual action Approve/Continue still uses
the original verified human request and the native grants.

`VerifyFlow` is a read-only local aggregate diagnostic. It validates generations,
budgets, ordered reviews, accepted bindings, actual native output and terminal
action evidence, then asks native diagnostics to reconstruct copied journals.
It requires exact execution/head coverage and zero new model/executor calls;
Canvas state and accepted refs are not written. Moving heads, active work,
legacy/incomplete history, failed executions or unavailable diagnostics produce
incomplete evidence, never synthetic success. Completed native responses rejected
by document validation can be reconstructed and counted through an explicit
same-step retry without inventing a human acceptance. Lowering the current
operator ceiling can make an older frozen-budget history incomplete; it never
rewrites that run's selected budget. The pinned native verifier cannot
reconstruct a denied tool execution even after explicit cleanup; that aggregate
remains incomplete. Remote Sessions refuses plaintext diagnostic fallback.

Flow-associated edits, MCP adapters and hosted revocation qualification remain
later work. Local abandonment is not proof of remote cancellation or absence of an orphan.
