# Go workflow runtime (local qualification)

The optional `OpenFlowRuntime` constructor runs compiled v1alpha3 workflows
through the existing native document harness. `OpenDefined` and command-line
loaders still refuse flow activation. There are no new HTTP, MCP or UI actions.
This is not hosted service-credential, grant, sign-in or deployment qualification.

## Host configuration

Pass the same dependencies as `OpenDefined`, plus `FlowRuntimeOptions`:

- `MaxAgentRuns`: a positive operator ceiling. Each Start supplies a positive
  budget no greater than this ceiling or the authored flow limit.
- `OwnerAuthorizer`: optional external `ReviewAuthorizer` for owner cancellation.
  It receives the verified principal, role `owner`, action `cancel`, current
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

Each service-owned attempt and consumed budget are journaled before dispatch.
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
owner, only at a quiescent gate or a known undispatched/completed-attempt pause.
Running, uncertain and action-stage cancellation is refused. Local simulation
accepts its original persona or owner persona without claiming authentication.
Canceled proposals remain history and cannot be accepted through a manual path.

A final action uses the accepted action-document proposal bound to this flow,
even when that document was gated earlier than the last agent step. It parks
under the engine service identity. Human approval records only; a separate
human Continue produces the simulated receipt. Denial ends the flow without an
automatic cleanup Resume. A human may explicitly Continue a known denial to
conclude the native session, even after a newer document revision; this produces
no receipt and does not re-execute the action. Recover is not a cleanup path.
Until that session concludes, a new flow is refused. Public action creation
cannot adopt a flow proposal.

## Failure and restart boundary

Ambiguous Create/Exec, Git publication or review bookkeeping pauses uncertain,
retains the active slot and never automatically retries. Opening state does not
dispatch. Nonterminal flows reopen uncertain, even if a proposal is visible;
this runtime does not infer permission to advance from historical acceptance.
Full reconciliation, generation replacement, cancellation/effect fences, edits
and aggregate replay are subsequent work. Local abandonment is not proof of
remote cancellation or absence of an orphan.
