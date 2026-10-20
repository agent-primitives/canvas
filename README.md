# Canvas

**Review what durable agents propose, inside the GitHub Copilot app.**

Canvas puts an agent's proposed document changes in front of a reviewer.
People decide what becomes accepted, Git keeps the document history, and
Sessions records how the agent did the work. Document acceptance, action approval
and continuation are separate steps, each tied to the work being reviewed.

For developers building human-review workflows, Canvas starts with a Copilot
review UI and grows toward shared documents, several agents and multiple
reviewers. The goal is a real approver and a replayable decision record.

**Pre-1.0:** today's local demo uses fictional drafts, real Git/Sessions records and simulated actions.
**Access:** one trusted operator on loopback; signed-in approval and shared access are still being built.

Canvas owns document review. [Sessions](https://github.com/agent-primitives/sessions)
records durable execution; [AgentSuite](https://github.com/agent-primitives/suite)
identifies portable agents; [kmx](https://github.com/kaimahi-agents/kaimahi)
authors, evaluates, packages and deploys them.

See the [architecture diagrams](docs/architecture.md) for today's local boundaries
and the planned hosted assembly.

## Try the smallest working path

From a normal checkout with Git and Go 1.27+ (toolchain 1.27.2 downloads enabled):

```sh
go run ./cmd/copilot-review
```

Open the printed loopback URL for the browser fallback, or follow
[the Copilot extension guide](docs/copilot-review.md) to open **Northwind Engine
Review** in a repository session (Node 26 required for its backend).
The demo supplies fixed responses using the Northwind definition, so you can
try it without a model download, provider key, container or cloud.
Use normal checkout modes; the fixture checks its suite inventory.

Create a writer draft, inspect both documents and accept or reject the complete
proposal. Then separately request action approval, record a decision and
explicitly continue the simulation. Check replay in the evidence controls.
The selected demo role is an attribution label; approval authority requires
verified identity, separately from Copilot sign-in.

Ctrl-C stops the engine. Private state remains in `.state/copilot-northwind`;
keep it on your own machine.

## What you need

See [model-backed prerequisites](docs/local-models.md#what-you-need), or use the
smaller prerequisite list for the deterministic review demo above.

## Quick start

For model-backed startup with `make dev`, see [local model setup](docs/local-models.md#quick-start)
and the [Northwind walkthrough](examples/northwind-prd/README.md). It selects
Northwind with embedded Sessions and separate `.state/northwind` state. The
review demo above supplies fixed responses instead. `make run` and the flagless
Canvas command use the same Northwind default with an already configured model.

## Try the Northwind workflow

Follow the [Northwind review steps](docs/local-models.md#try-the-northwind-workflow)
and [worked example](examples/northwind-prd/README.md).

## Trying the legacy Draft Room

The football backend and example have been removed. Existing legacy workspaces
are not migrated, reset or reactivated; selecting them explicitly is refused.
Historical session formats and archived contracts remain readable as described in
[runtime compatibility](docs/runtime-upgrade.md). Keep old state private and backed
up; see [state and backup guidance](docs/local-models.md#resetting).

## Using a hosted model instead

Use the [hosted-model configuration guide](docs/local-models.md#using-a-hosted-model-instead)
for endpoint selection, private key files, metadata checks and charge warnings.

### Diagnosing an endpoint without generating

Use the [metadata-only endpoint checks](docs/local-models.md#diagnosing-an-endpoint-without-generating)
before starting a model-backed run.

## Resetting

See [separate state paths and backups](docs/local-models.md#resetting).

## Upgrading existing state

Read [backup and upgrade precautions](docs/local-models.md#upgrading-existing-state)
before opening existing state with a newer runtime.

## Development

See [development and verification](docs/development.md) for test/build requirements,
deterministic browser checks and model-qualification limits.

## More

- [Docs index](docs/README.md): choose the deterministic, model-backed, authoring or recovery path.
- [Roadmap](ROADMAP.md): authenticated AKS review, shared work, and later frontend choices.
- [Copilot review guide](docs/copilot-review.md): setup, workflow and trust limits.
- [Definition authoring](docs/canvas-authoring.md): draft files and validate before activation.
- [Generic Canvas](docs/generic-canvas.md): documents, scopes, recovery and exact-snapshot approval.
- [Model-backed walkthrough](examples/northwind-prd/README.md): run the Northwind definition with a model instead of fixed responses.
- [Released-suite fixture](examples/northwind-released/README.md): agent identity and unsigned evaluation consistency.
- [Runtime compatibility](docs/runtime-upgrade.md): backups and migration limits.

A Copilot authoring plugin and Claude Artifacts front end are **planned, not
started**, after the first public release. Both will use the same engine.

## Contributing

This project welcomes contributions and suggestions.  Most contributions require you to agree to a
Contributor License Agreement (CLA) declaring that you have the right to, and actually do, grant us
the rights to use your contribution. For details, visit [Contributor License Agreements](https://cla.opensource.microsoft.com).

When you submit a pull request, a CLA bot will automatically determine whether you need to provide
a CLA and decorate the PR appropriately (e.g., status check, comment). Simply follow the instructions
provided by the bot. You will only need to do this once across all repos using our CLA.

This project has adopted the [Microsoft Open Source Code of Conduct](https://opensource.microsoft.com/codeofconduct/).
For more information see the [Code of Conduct FAQ](https://opensource.microsoft.com/codeofconduct/faq/) or
contact [opencode@microsoft.com](mailto:opencode@microsoft.com) with any additional questions or comments.

## Trademarks

This project may contain trademarks or logos for projects, products, or services. Authorized use of Microsoft
trademarks or logos is subject to and must follow
[Microsoft's Trademark & Brand Guidelines](https://www.microsoft.com/legal/intellectualproperty/trademarks/usage/general).
Use of Microsoft trademarks or logos in modified versions of this project must not cause confusion or imply Microsoft sponsorship.
Any use of third-party trademarks or logos are subject to those third-party's policies.
