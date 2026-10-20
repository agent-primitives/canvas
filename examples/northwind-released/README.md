# Northwind: released suites and unsigned evidence

Opt-in fixture for reviewing released AgentSuite identity in the browser and
Copilot canvas. The original `northwind-prd` example is unchanged.

For setup authoring rather than this review demo, use the sibling
[manual-only starter](canvas-starter.yaml) and [authoring guide](../../docs/canvas-authoring.md).
It reuses these assets without duplicating blobs; validate it with
`go run ./cmd/canvas --validate --definition examples/northwind-released/canvas-starter.yaml`
from the repository root. Validation does not start or authorize anything.

```sh
go run ./cmd/copilot-review \
  -definition examples/northwind-released/canvas.yaml \
  -state .state/northwind-released -listen 127.0.0.1:18878
```

Open the printed browser URL, or use the Copilot extension described in
[the local review guide](../../docs/copilot-review.md). Expand **Released suite
evidence** in Copilot, or an agent's **Suite identity** disclosure in the browser.
New writer draft still requires explicit document review; acceptance never
approves or continues the simulated announcement.

This command's provider is deterministic: **no real model calls or external
effects**. Its local Canvas harness is not the native runtime named by the
released evaluation. The two identities are displayed separately.

## What was actually evaluated

The detached evidence was produced by the unchanged **kmx v0.8.0** release binary
through real workspace evaluation, managed proof capture, cleanup and
`kmx eval export`. The external Kubernetes transport, native runtime inventory
and Task results were **mocked deterministically**. No Kubernetes cluster,
Orka/AgentKit container, real model or Canvas harness was evaluated.

Each suite has two cases and two runs, using text `contains` assertions without
judges or retries. They check fictional document/review markers, rollout and
isolation/rollback limitations, and mock disclosure. These are transport/proof/
export consistency fixtures, not model-quality or complete Northwind evaluations.

The successful label is **evaluation consistent; unsigned**. It means that
`agentsuite.VerifyEvaluation` matched the retained layout, independently reviewed
expectations, case/run coverage and passing claims. It does not establish who
made those claims, whether observations were real, clock authenticity, freshness,
Canvas runtime equivalence or deployment readiness. Replay is a different check.

## Pins and identity domains

| Member | Exact OCI artifact digest S |
| --- | --- |
| writer | `sha256:b9513f8f72b41ea36fde2179da726f62d1d28412402621023cfc0f23cadb3544` |
| reviewer | `sha256:6ea51f953d1d51be7316fb155cbce783e7010afb43169e479f44e0cce2783ccc` |

Both select `linux/amd64`, the unprofiled/native composition, and its
`agentkit-v0.1.0` build profile. The associated Python base and
`serve-pydantic-ai` harness image pins are **build metadata, not the tested native
Orka runtime or this Canvas harness**. No associated images ran. The newly
authored model is `northwind-mock`; the original Northwind model was not overridden.

The library module is `github.com/agent-primitives/suite v0.4.0`; the authored
schema version remains `1.0.0-draft`. Neither version is an artifact, graph,
member, composition, build-profile, evidence or execution-archive digest.

Producer details:

- kmx module `v0.8.0`, clean VCS revision
  `b071bcfb37afaffaab7c84553a36aa2d1f05fdbf`.
- Exact Linux/amd64 release executable SHA-256:
  `c7f7defcc1f40d32e779d48e66a8e9a549cd3c23f85ccbdfc2ecaea1a590d3f6`.
- Expected evaluator version:
  `v0.8.0-git-b071bcfb37afaffaab7c84553a36aa2d1f05fdbf`.
- Native accepted policy pins Orka controller `0.2.0` and harness `harness-v2`.
  Their digests are in the expected files. These were mock inventory inputs,
  not proof that the images executed.
- Expected files were derived from the pinned artifact and released producer,
  model/execution/text-policy projections, without reading evidence or private
  receipts. They are independent reviewed inputs, not authoritative claims
  copied from the evidence. `judges: []` explicitly allows no judge configuration.

## Offline verification and deliberate mismatch

With released kmx v0.8.0:

```sh
kmx eval verify \
  --suite sha256:b9513f8f72b41ea36fde2179da726f62d1d28412402621023cfc0f23cadb3544 \
  --from-layout examples/northwind-released/layouts/writer \
  --expect examples/northwind-released/evidence/writer.expected.json \
  --evidence examples/northwind-released/evidence/writer.export.json
```

For the refusal demonstration, stop the previous command and use separate state:

```sh
go run ./cmd/copilot-review \
  -definition examples/northwind-released/canvas-mismatched.yaml \
  -state .state/northwind-release-mismatch -listen 127.0.0.1:18878
```

The writer shows **not verified (subject)**. Its evidence is an unchanged copy
of the genuine reviewer export, deliberately paired with the writer artifact
and expectations. A mismatch never becomes a passing evaluation, including
after replay. This does not prohibit local document proposals: evaluation is
informational, not an execution/approval grant. An invalid artifact pin,
ambiguous selection or unsupported execution requirement instead refuses loading.

New declared public runtime-profile execution is not supported. kmx v0.8.0 cannot
export for those profiles; the evidence refusal is **not verified (export
unavailable for this profile)**. Canvas refuses incompatible profiles before
harness registration; unsigned evidence cannot make them executable.
