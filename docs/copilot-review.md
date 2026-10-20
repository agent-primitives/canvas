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

The fixture reads the checked-in Northwind definition and suites. Their captured
inventory includes file modes, so use a normal checkout with 0644 source files
and 0755 directories, not a checkout created under umask 077. Do not weaken inventory validation or
rewrite artifact pins to accommodate different modes.

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
