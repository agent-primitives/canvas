# Canvas

When an agent changes a document, people need to see what changed and decide which version to keep.

> An agent drafts a fictional product plan and an announcement.
>
> You compare both proposed documents with the current versions.
>
> You accept or reject the whole proposal.
>
> Publishing needs a separate approval. The demo simulates it without sending anything.

Canvas is a document-review workspace for developers building human review into
agent workflows. Git keeps the accepted document history, and
[Sessions](https://github.com/agent-primitives/sessions) records agent runs and
decisions. It is not a shared service or a live publishing tool.
It also runs as a panel in the GitHub Copilot app through an optional
[local extension](docs/copilot-review.md).

**Status: pre-1.0. The local demo supports one trusted operator, uses fixed responses and simulates actions.**

## Try it locally

You need Git, Go and a browser. See the [Go requirements](docs/local-models.md#what-you-need)
if you do not have Go installed.

```sh
git clone https://github.com/agent-primitives/canvas.git
cd canvas
go run ./cmd/copilot-review
```

Open the printed URL in your browser. Use **New… → New agent run** to request a
draft from **PRD writer**. Inspect both documents, then accept or reject the
proposal. No model download, provider key, container or cloud account is needed.

Accepting documents does not approve publication. To try the rest of the demo,
request action approval, record a decision, then explicitly continue the
simulation. The [browser workflow guide](docs/local-models.md#try-the-northwind-workflow)
explains the review steps and replay limits; this demo uses fixed responses instead
of a model.

Press Ctrl+C to stop. Local state remains in `.state/copilot-northwind`.
Use fictional data and keep that state private. Demo roles and Copilot sign-in do
not verify who approved a change.

## Go deeper

- [Review inside the GitHub Copilot app](docs/copilot-review.md): optional extension setup and the demo's limits.
- [Run with a real model](docs/local-models.md): local or hosted setup, costs and state precautions.
- [Review release notes with two agents](docs/review-release-notes-with-two-agents.md): writer/reviewer walkthrough using a real hosted model.
- [Make your own review workspace](docs/your-first-review-canvas.md): a model-free walkthrough using your own starting document.
- [Architecture](docs/architecture.md) and [current limits](docs/README.md#current-limits): document history, approvals, execution and planned shared access.
- [Development and verification](docs/development.md): build, test and browser checks.
- [All documentation](docs/README.md) and [roadmap](ROADMAP.md).

See [SUPPORT.md](SUPPORT.md) for help and [SECURITY.md](SECURITY.md) for private
vulnerability reporting. This project uses the [MIT License](LICENSE).

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
