# Canvas docs

Start with the [overview](../README.md) and [roadmap](../ROADMAP.md).
Canvas is pre-1.0: the checked-in review demo is local, fictional and
unauthenticated. Copilot sign-in and demo roles are not Canvas review authority.

## Choose a path

- **Try document review without model setup:** [Copilot review demo](copilot-review.md).
  Fixed responses, real local Git/Sessions records, separate document review,
  action approval and continuation. The browser fallback is also available.
- **Run with a model:** [local or hosted setup](local-models.md), then the
  [Northwind walkthrough](../examples/northwind-prd/README.md).
  Model calls may take time or incur charges; publication is still simulated.
- **Review release notes with two real agents:** [tutorial 2](review-release-notes-with-two-agents.md).
  Fictional envctl sources, whole-proposal review and a separately approved local
  announcement simulation. Requires the suites' exact configured hosted model.
- **Draft your own definition:** [authoring and offline validation](canvas-authoring.md).
  Review the file diff, scopes and triggers before deliberate activation.
- **See the system boundaries:** [architecture diagrams](architecture.md) separate
  the current local engine from the planned hosted assembly.
- **Understand the engine:** [generic Canvas](generic-canvas.md) describes
  documents, schemas, inputs, read/propose scopes and bounded recovery.
- **Inspect released-agent evidence:** [released-suite fixture](../examples/northwind-released/README.md).
  Unsigned evaluation consistency is separate from runtime qualification and replay.
- **Preserve state:** [runtime compatibility and backup](runtime-upgrade.md)
  and [local state paths](local-models.md#resetting).
- **Develop or test:** [development guide](development.md), including browser
  tools, offline tests and model-qualification limits.

## Related projects

Canvas owns document review. [Sessions](https://github.com/agent-primitives/sessions)
records durable execution; [AgentSuite](https://github.com/agent-primitives/suite)
identifies portable agents; [kmx](https://github.com/kaimahi-agents/kaimahi)
authors, evaluates, packages and deploys them.

The current review UI works in a browser or the GitHub Copilot app. Shared
workspaces, several reviewers and signed-in approval are still being built.
A Copilot authoring plugin and Claude Artifacts front end are planned, not
started; see the [roadmap](../ROADMAP.md).

## Current limits

Only a single trusted local operator is supported. The signed-in AKS review
path and multi-reviewer workspace are [roadmap stages](../ROADMAP.md), not a
supported shared-service installation. Keep state, prompts and model output private.
Prototype capabilities and UI confirmations do not prove human identity.

`make dev` starts the generic Northwind example with embedded local Sessions and
separate `.state/northwind` state after model setup/metadata checks. Existing
legacy state is neither migrated nor reset. `make run` and the flagless Canvas
command select the same Northwind definition and state, but need an already
configured model endpoint. The standalone Sessions host uses separate
`.state/northwind-host` state. None of these starts the Copilot review flow;
use the commands in the selected guide.

Cluster deployment is not a supported setup path. The local instructions above
do not qualify Northwind or AKS deployment.
