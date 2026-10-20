# Northwind PRD walkthrough

Turn fictional feedback into reviewed documents, then approve one exact
announcement snapshot for a **local simulation**. Nothing is sent or published.
Personas label who acted; they are not accounts or authentication.

## Start locally

Follow the [model-backed local setup](../../docs/local-models.md#quick-start) to prepare
the pinned Ollama model, check metadata and start Northwind with `make dev`.
For a direct start once the model is ready, use:

```sh
make northwind MODEL_PROFILE=offline
```

This command uses this directory's `canvas.yaml`, an embedded local Sessions
host, and separate `.state/northwind` state. It does not start Ollama itself.
Open the printed URL (default http://127.0.0.1:8765) and look for
**Northwind fictional PRD**. No cloud account is needed. Alternatively, configure
an existing [HTTPS model endpoint](../../docs/local-models.md#using-a-hosted-model-instead)
first, then use `MODEL_PROFILE=hosted`; its generation may incur charges.

`make dev`, `make run` and the flagless Canvas command select this example and
`.state/northwind`. Only `make dev` prepares the local model before startup.
Existing football state is preserved, not adopted into this workspace.
Stop one app before starting another on the same HTTP port. Starting Northwind
and viewing seeds do not generate an answer. CPU generation can take minutes;
there is no latency or model-quality guarantee.

## Write, review and revise

1. Read **Fictional project brief** and **Fictional observations**. The initial
   documents are context, not verified customer observations or measured results.
2. Select **New… → New agent run → PRD writer**. Try:

   > Draft a fictional pilot PRD and announcement draft. Keep rollback and opt-in
   > criteria explicit. Do not invent measured duration improvements or claim
   > that anything has been published.

   Select **Request run**. The writer proposes whole-document replacements for
   **Proposed PRD** and/or **Simulated announcement — never sent**. A model result
   that violates the schemas or scope is refused, not partially applied. You may
   need another explicit attempt; the tests do not qualify model output quality.
3. Select **Review affected documents**. Inspect every replacement, then Accept
   or Reject the complete proposal. Accept creates a new accepted revision; it
   does not send an announcement or approve a native action. Agents cannot accept
   their own work.
4. Explicitly request **Independent reviewer** against that accepted revision:

   > Review the accepted PRD for unsupported claims, missing rollback criteria
   > and unresolved risks. Do not treat announcement copy as measured evidence.

   This is a separate one-member suite, not a hidden orchestrated handoff. It
   reads the accepted PRD and recorded feedback, not the pending writer proposal.
   Review its proposed **Independent review** document normally.
5. To revise from feedback, select **New… → Add source file**, use path
   `feedback/new.json`, and enter:

   ```json
   {"team":"Northwind","feedback":"Fictional follow-up: rollback and opt-in criteria remain unresolved. No duration improvement has been measured."}
   ```

   Select **Save source file**. Valid changed feedback is checkpointed and the
   file trigger can start a writer run. Inspect runs before requesting another:
   a save or retry can incur a hosted model charge. A changed read input makes
   pending proposals stale; regenerate explicitly against the new accepted base.

## Approve only the pinned announcement

1. Have the writer propose an announcement and accept its document proposal.
   A seed or an unaccepted proposal cannot create an approval gate.
2. In **Local publication**, select the announcement action and the accepted
   proposal that changes its document, then **Request approval**.
3. Read the exact pinned text, revision and digest. Check the confirmation and
   choose **Record approval** or **Record denial**. This records only a native
   decision; it does not run the simulation.
4. Inspect again, check the newly cleared confirmation, and explicitly choose
   **Continue simulation**. A successful native receipt says
   **SIMULATED_ANNOUNCEMENT: recorded locally; nothing sent or published**.
   A denial or executor error is not a successful announcement receipt.

Approval authorizes only those immutable bytes. A newer accepted version needs
its own request and approval, even when its text is identical. When one exists,
the UI warns:

> A newer version exists; approving publishes this older version

The same explicit confirmation is required for both current and older snapshots.
Continue uses the pinned snapshot, never a replacement from the current document.
Approval never carries forward. A changed inspection clears consent, and a stale
confirmation refuses rather than silently switching versions.

## Questions, replay and recovery are different

| Control | What it does |
| --- | --- |
| **Ask agent why** | A new read-only model turn on a supported recorded session. May incur charges; cannot propose or publish. Invalidates previous Replay proof. |
| **Replay** in Evidence | Zero-model reconstruction/verification of the journal. Not Resume, evaluation, or a claim that the document is correct. |
| **Resume / Confirm Resume** | Explicitly re-sends identified pending tool-free model work. **Re-sends the last model request; it may be charged again.** |
| **Continue simulation** | A separate confirmed continuation of a native approval decision, not a retry of the writer's model request. |

Startup, polling and reopening the page never decide, resume or resend work.
See the [recovery and approval guide](../../docs/generic-canvas.md#approve-an-exact-announcement-snapshot)
for uncertain dispatch, denial and explicit recovery limits. Preserve historical
state. A changed definition refuses incompatible state instead of reinterpreting
old approvals; use a fresh separate state directory when required.

## What the example proves—and does not

Each suite has one tool-free member. The writer proposes PRD and announcement
**drafts**. The native approval action is a separate zero-model harness, not tools
added to those suites. Generic Canvas is embedded-local-only: no remote Sessions,
Substrate templates, connectors or external publication are supported here.

The UI reports **evaluation: not verified**. Suite identities and Replay proof are
not evaluation evidence. The native current-format manifests were authored with
public AgentSuite v0.1.0 types and canonical Marshal. Adjacent layouts were
produced offline using KMX
`suite push <content> --to-layout <layout> agentsuites/northwind-<member>:v1`.
Both platform compositions are provenance only. Their required build-profile
image descriptors deliberately use synthetic
`example.invalid/canvas/not-executed` references: they are not runnable-image
claims, are never pulled, and do not qualify execution or evidence. Canvas uses
its own local model-only execution. Format/content consistency is not security
certification, model behavior, evaluation, or authentication.

For authoring another definition, see the
[definition and document limits](../../docs/generic-canvas.md#definition-and-document-limits).
Globs are read-only inputs and support only basename `*.suffix`, such as
`feedback/*.json`; proposal scopes name exact output document IDs, never globs
or arbitrary filesystem paths. Keep `.state/` private: it contains prompts,
model output and local history, not just this fictional seed data.
