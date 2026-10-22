# Local Northwind review in Copilot canvas

This optional demo uses Canvas's existing document engine, Git store and custom
embedded Sessions host. By default, an in-process deterministic fixture supplies proposals:
**no model service, provider credentials, Ollama, container or cluster is used**.
It is not a suite evaluation or an authenticated approval product by default.
For the engine's separate execution/human credential interfaces and their
local fake-auth qualification, see [authenticated sessions](authenticated-sessions.md).
The CLI's user-token configuration alone cannot supply the required service binding.
For explicit real providers or a separate unauthenticated trusted-loopback Sessions
connection, see [native document producers](producer-harness.md). These modes do
not inherit the fixture's zero-model-call or local-replay claims.

To draft a new definition without starting an engine, use the separate
[authoring starter and offline validation guide](canvas-authoring.md).

### Request a scoped document edit

A local v1alpha3 definition with a compiled `editAgent` binding exposes the edit
panel. Select an accepted document, inspect its exact revision/digest and full
content, choose whole-document, JSON Pointer, or supported ATX heading scope,
and supply an instruction. This creates a proposal, **not accepted bytes**.
Review every replacement in the existing proposal review before Accept/Reject.
A missing or invalid explicit selector refuses rather than becoming a whole edit.
The deterministic Northwind fixture edits only the PRD title and does not
interpret instructions or qualify live inference.

The single durable slot fences competing decisions and Continue. A lost reply
retains the exact request or maintenance command for explicit continuation;
polling, opening and reopening do not retry, publish, accept or dispatch work.
Document-only requests refuse while any nonterminal flow is active or the same
document has an unsettled manual action; editing never retires that manual handle.
With an eligible current flow selected, the panel explicitly associates the
inspected context and request with that flow. For an associated edit,
Reject/failure preserves accepted bytes and requires explicit Resume. Accept invalidates old-base proposals and
that flow's affected unexecuted action handles, then requires an explicit
first-step Rerun using the same remaining flow budget, without inherited approvals.
Completed/effect-committed flows use a separate document-only edit; its acceptance
never starts or reopens a flow.

The minimum durable request/native/Git publication and review recovery also
protects standalone document-only edits. Explicit Reconcile inspects exact evidence;
a proven uncommitted review restores a proposal for fresh whole-proposal human
review, never replays the decision. An abandoned edit may also use read-back-only
reconciliation to prove its exact owned native END. Optional native settlement
clears only admission bookkeeping, never publishes abandoned output or accepts,
dispatches or resumes work. Missing/unknown execution identity remains fenced.
The same proof applies to flow-associated abandoned attempts, not just standalone
ones. Native Cancel remains unsupported; local abandonment/revocation is not
native settlement, and unresolved native action work still blocks rerun.
See [edit boundaries and replay](flow-runtime.md#scoped-document-edit-proposals)
for selector support, edit-only number guards, limits, local-only APIs and
incomplete-proof cases.
Chat drafts reviewable file diffs; it does not activate, approve or continue.

## Start the engine

From this repository, with Go and Node 26 installed:

```sh
go run ./cmd/copilot-review
```

The engine listens on **127.0.0.1:18878** and uses separate private state at
`.state/copilot-northwind`. The existing browser remains available at
`http://127.0.0.1:18878/`. No change to normal Canvas startup or existing state is
required. A port collision refuses startup; do not stop another service by name.
The optional `-state` path must be a dedicated demo directory. Do not point it at
another Canvas or prototype's state. `-listen` supports isolated tests, but the
Copilot extension expects the default port.

The fixture reads the checked-in Northwind definition and suites. Suite v0.6 uses
portable loose-file identity: ordinary files default to 0644 and directories to
0755; executable modes come from declarations, not host chmod bits. Artifact
bytes and pins are unchanged. Older retained suites with nondefault ordinary-file
modes such as 0600/0755 can refuse reopening; there is no automatic history
migration, repacking or pin repair.

## Opt into the local flow panel

The manual demo above is unchanged. To use the declared draft → critique → revise
workflow and its separate simulated-action gate, start a **new, dedicated instance**:

```sh
go run ./cmd/copilot-review -definition examples/northwind-prd/flow.yaml \
  -state .state/copilot-flow -flow-agent-runs 12
```

Open the same extension described below. **Run on a topic** submits structured
inputs and a selected attempt budget, bounded by both the definition and the
operator ceiling. The starting accepted revision and definition digest are shown.
Optional schema defaults are hints, not silently inserted values. Runs share this
workspace's accepted documents; use a separate state directory for an independent
topic. The deterministic fixture reads the actual native topic request; its
revision writer reads the accepted draft and accepted critique. It is not inference
or evaluation evidence. Explicit embedded model profiles remain available under
the existing producer restrictions; external/authenticated Sessions flow mode is
not enabled by this option.

The panel shows ordered agents, required documents, human gates and the simulated
action. Waiting roles are **unverified local attribution**, not work-account
sign-in or a claim that a particular person can approve. Review every replacement
in the proposal, including additional allowed documents. Accepting a document
advances only its bound current gate. Request changes records feedback and consumes
another attempt; failures and retries also consume the selected budget.

The final action is prepared by the engine, not the manual “Request action
approval” control. Recording approval still runs nothing: inspect and confirm the
exact snapshot again before **Continue simulation**. A receipt is local only;
nothing is sent or published. Refresh/reopen reads state and closing the panel does
not cancel. After an engine restart, reopen the panel for a fresh capability;
**Reconcile** inspects durable evidence and **Advance** is a separate explicit
command. Unknown work is never automatically redispatched. Native cancellation
may be unsupported: a persisted cancellation fence is not proof of remote
cancellation or settled execution. Aggregate replay reports incomplete coverage
honestly and only shows 0/0 when actual linked native proofs establish it.

This path uses an operator-loaded definition. For frozen draft activation use the
separate mode below. Version comparison, MCP and agent-callable SDK actions
remain absent. No visual redesign or actual Copilot app/WSL, hosted
identity, real-action or cloud qualification is implied.

## Freeze, review and activate a draft

Keep drafts outside runtime state and watcher/input directories. With a new,
dedicated state directory, the local operator can open an **inactive** shell:

```sh
go run ./cmd/copilot-review -draft-root /path/to/review-draft \
  -state .state/copilot-authoring
```

For a flow-bearing package, also supply a positive operator ceiling, for example
`-flow-agent-runs 12`. This mode refuses `-definition` and remote/authenticated or
trusted Sessions combinations. Existing operator startup and manual defaults do
not change. Generated manual templates use their qualified deterministic fixture. Northwind's
fixture requires its reviewed schema bytes, document/agent scopes and suite pins;
metadata, valid seeds, gates and budgets may change. Other shapes or suites require
a supported operator provider configuration. Binding
and validation do not qualify arbitrary models or runtime profiles.

Open the installed panel below. **Prepare frozen package** explicitly captures the
selected definition and its transitive schemas, seeds, evaluation references and
complete local suite layouts. It copies no unrelated workspace files. The limit
is 1,024 files, 32 MiB total and 8 MiB per file, with stricter compiler limits still
applied; unsafe paths, symlinks, incomplete or corrupt dependencies refuse. Wrapper
files use portable 0644/0755 modes; embedded artifact bytes remain exact. Reads,
reloads and source edits never prepare, activate or start Sessions automatically.

Review the whole inventory, package and definition digests, suite bindings, and
exact text/binary downloads before checking confirmation and choosing **Activate**.
Activation and restart compile the retained bytes with no registry/cache resolver;
a later working-file edit cannot change that instance. Deterministic provider,
mode, digest and state checks happen before retiring a predecessor. Flow packages
refuse at a zero operator ceiling. Activation itself does not run an agent; use
the existing explicit workflow/document controls after fresh inspection.

Each activation creates fresh SharedSpace/Sessions/Canvas state from the reviewed
seeds. A predecessor must have settled work: finish/reject/cancel/reconcile queued,
uncertain or undecided work first. Activation never settles native effects or
silently cancels work. Successful retirement makes the predecessor read-only, with
reciprocal stable links. Its accepted bytes/revision/digests, proposals, history,
flows and recorded native evidence remain available without reopening its engine
or reconstructing a new proof. Normal writable reopen refuses the durable seal.
No documents, proposals, approvals, budgets or Sessions IDs carry over. Future
carry-over must explicitly create new reviewed proposals; no import action exists.

A lost activation reply retains the exact candidate, predecessor and request key.
Inspect the catalog; only explicit same-request **Continue activation** may resume
pending startup. A pending restart never retries automatically or mints a new
instance. If activation committed but this bridge did not observe the result,
reopen the panel for the recorded active instance. Failure after a durable seal
keeps the predecessor read-only and the reserved successor pending; it never
unseals or falls back to mutable source. Missing/corrupt retained bytes refuse.
No archive/package cleanup or migration occurs automatically.

These clicks and labels remain local unverified attribution—not authentication,
human-only containment, hosted identity or real-action qualification. There is no
activation CLI command or agent-callable activation/decision tool.

## Load the separate extension

You control Copilot installation, sign-in, environment selection and extension trust.
No sign-in assertion is consumed by this demo. The extension is at
`.github/extensions/northwind-review/`, with its own ID **northwind-engine-review**.
It does not replace another installed review extension.

Copilot supports repository extensions under `.github/extensions/`. **Open a
Copilot session in this repository's folder before looking for the canvas**;
opening the app alone is not sufficient for discovery in the tested setup.
Use the intended local environment and approve only this extension's trust
prompt. If using a personal installation instead, copy the **entire**
`northwind-review` directory into that environment's personal
`~/.copilot/extensions/northwind-review` directory, only when that destination is
absent. Never copy into or overwrite an existing extension directory. The SDK
`@github/copilot-sdk/extension` is supplied by the Copilot host; no npm install is
needed in the extension directory.

In a Windows app with the engine in WSL, the extension backend must reach WSL's
literal IPv4 localhost port. This requires an actual app/environment check; a
headless browser test does not qualify the Copilot SDK, discovery or Windows/WSL
placement. Do not add a public tunnel or wildcard listener to work around access.
Open **Northwind Engine Review** from the installed canvases, not an agent-generated
replacement. Reopen the canvas after an engine or extension restart to obtain a
fresh backend and capability. Refresh cannot restore a capability stripped from
the URL. The adapter holds the engine's local cookie server-side and exposes only
the review routes to its iframe; normal Canvas browser guards are unchanged.

## Review loop

1. **New writer draft** creates one deterministic provider call and a native
   recorded run. It proposes the PRD and announcement together. Inspect both
   readable comparisons, raw diffs and exact bytes, then confirm whole-proposal
   review. JSON comparisons preserve numeric values; YAML uses line comparison,
   not YAML schema validation. Ambiguous or oversized comparisons visibly fall
   back to raw lines. All comparisons use the proposal's original base.
2. **Reject documents** preserves accepted Git documents. **Accept documents**
   changes the accepted Git snapshot only. Neither creates an action or records
   publication approval. Conflicting proposals become stale and cannot be accepted.
3. For an accepted announcement, **Request action approval** creates the separate
   native simulated-announcement request. Read its pinned text and evidence, then
   confirm the exact snapshot and pending request.
4. **Record action approval** or **Record action denial** records only a native
   decision. Approval says **Recorded; not executed**. The confirmation clears.
   No receipt or continuation is implied.
5. Inspect and confirm again, then **Continue simulation** for approval or
   **Conclude without simulation** for denial. If an inspection is in flight,
   the click waits for it; unchanged evidence permits one dispatch, while changed
   evidence cancels it and requires review and confirmation again. Approval produces a bound native
   `SIMULATED_ANNOUNCEMENT` receipt; denial concludes without a successful receipt.
   Nothing is sent or published outside the machine.
6. Completed document review collapses to a reopenable summary; every changed
   document remains available. **Verify draft replay**, under **Run evidence and
   diagnostics**, reconstructs the recorded proposal execution.
   **Verify action replay**, under **Snapshot and native evidence**, is available
   for a completed successful simulation.
   A 0/0 label requires chain/output checks, completed execution coverage, and
   explicit zero model **and** executor calls from the host diagnostic. An older
   host lacking executor/coverage evidence never becomes a 0/0 claim. Denied or
   unfinished action executions do not receive a successful replay claim.

Repeated fixture drafts can contain equal text; a new accepted revision still
requires its own action approval. Existing Canvas behavior permits an older
accepted snapshot after displaying **A newer version exists; approving publishes
this older version** and obtaining fresh confirmation. It never substitutes the
latest content or transfers approval to another revision.

Connection loss marks retained content **last known** and disables decisions.
Offline reads are not automatically retried: check the engine, then use
**Refresh connection** for the same running engine. After a restart, address
change or lost capability, reopen the installed canvas instead. Refresh cannot
renew that view. An uncertain write may already have been recorded; inspect
its outcome rather than repeating it.

Stopping and restarting the engine preserves Git/SQLite evidence and pending
native requests. Startup and polling do not resume them. Uncertain requests are
not retried automatically; inspect the existing browser's recovery controls
rather than creating a replacement effect. Provider-call totals describe this
boot, not a durable billing ledger. Replay proofs describe copied journal heads,
not model quality, evaluation success or new approval authority.

## Boundaries and verification

Identity is **unauthenticated (prototype)**. There are no agent-callable extension
actions. The loopback capability, same-origin checks and UI confirmations are not
human authentication or isolation from local processes that can reach HTTP/gRPC.
Use fictional data and a single trusted local operator. Real effects/shared use,
authentication, new runtime-profile execution, generic fork creation, kmx Lift and
cloud deployment are deliberately outside this demo. The optional
[released-suite variant](../examples/northwind-released/README.md) adds pinned release
identity and unsigned exported-evidence consistency; its native transport was mocked,
and it does not qualify this Canvas runtime.

`make test` includes extension syntax, adapter-boundary tests and an actual
Chromium iframe test against the Go/Git/gRPC/SQLite engine. The iframe test needs
the same Playwright dependency as the existing browser tests and uses independent
scratch state and assigned ports. It covers document rejection/acceptance,
explicit denial/approval, separate Continue and replay 0/0. It is not an actual
Copilot app qualification.

## Execution ownership and attribution

New local attempts and simulated actions use the stable, unverified engine
principal `demo:canvas-engine`, never the selected persona as owner. Persona
labels remain attribution, not human authentication. Old ownerless or human-owned
sessions remain history; start a fresh attempt rather than silently re-owning or
resuming them.

With an explicitly configured authenticated host, service credentials handle
execution and evidence reads; verified human credentials handle Approve and
Continue directly. Requester and accepting reviewer are separate provenance.
The [credential and fake-grant contract](authenticated-sessions.md#execution-ownership-and-human-decisions-local-fake-contract-only)
explains the trusted binding seam, deliberate CLI refusal, legacy-state rules and
hosted qualification limits. No service-token provider or hosted grant support is
claimed by this local demo.
