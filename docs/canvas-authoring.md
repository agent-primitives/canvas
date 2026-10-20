# Draft a Canvas definition, review it, then validate offline

Copilot chat can draft setup files for a human to review as a repository diff.
It is the configuration drafter, not the activator or approver. Document
proposals still belong to recorded Sessions-run agents; Copilot chat history
is not their durable execution journal.

For a new portable fixture-backed package, see the [init and offline validate
commands](authoring-cli.md). They do not start a runtime or add Copilot drafting.

## Start with the manual-only template

Use [canvas-starter.yaml](../examples/northwind-released/canvas-starter.yaml).
It lives beside the existing released Northwind assets, reuses their exact
writer/reviewer pins, and adds no copies of the OCI blobs. Both triggers are
manual. The writer can read only the brief, feedback and PRD and propose only
the PRD and announcement document; the reviewer can read the same inputs and
propose only the review. An announcement is just a draft document here:
**no actions or event triggers are declared**.

From the repository root, validate the unmodified starter without starting it:

```sh
go run ./cmd/canvas --validate \
  --definition examples/northwind-released/canvas-starter.yaml
```

For edits in this checkout, save a new sibling definition, such as
`examples/northwind-released/canvas-draft.yaml`; do not overwrite the released
example. The filename need not be `canvas.yaml`. In a separately packaged
project, the reviewed draft can be named `canvas.yaml`.

## Give Copilot a bounded drafting request

Supply the starter and the relevant existing files, then ask, for example:

> Draft a new Canvas definition as a file diff using this manual-only starter.
> Prefer its existing released-suite pins; do not invent new pins or change
> agent/model identities. List the documents, schemas and seed files needed.
> Keep each agent's read/propose scope explicit and as narrow as its role needs.
> Keep every trigger manual; omit action declarations and event triggers.
> Explain any requested scope change separately for review. Do not start or
> activate Canvas, approve anything, continue a run, or call engine write routes.
> Do not add agent-callable activation, approval or continuation actions.

Review the complete diff, including newly created files, rather than approving
a chat summary. If different agent behavior is needed, use **kmx for agent/suite
files only**, inspect those changes and review the resulting released identity
before updating the Canvas pin. Canvas owns definitions/documents/triggers;
there is **no `kmx canvas`**. Existing released pins are preferred over rebuilding
agents merely to author a definition.

## Package the assets, not just the YAML

`--definition` is resolved relative to `--root` (the current directory by
default), unless absolute. Inside the definition, schema, seed, suite-layout and
optional evaluation-file paths are relative to the **definition's directory**.
Those source paths must be portable relative paths: no absolute paths, `..`,
hidden path components or symlinks. Moving only the YAML to another directory
and pointing back with `../` is not supported.

For the maintained starter, keep it next to the existing `layouts/`, `schemas/`
and `seeds/` directories: no duplicate blob tree is needed in this repository.
When distributing a separate package, include those directories with their
relative layout intact, including the complete OCI layouts and referenced
blobs. A digest alone does not supply the offline bytes. Do not edit layout
blobs or rewrite pins to accommodate local files. If adding detached evaluation
references, include their reviewed expected/evidence files too. The starter
omits them; see the [released fixture's evidence limitations](../examples/northwind-released/README.md).

## Review and validate without running

Before any deliberate human startup:

1. Inspect every document path, schema and seed. Keep fictional inputs; do not
   grant access to private files just to make validation pass.
2. Check every agent's exact suite pin, member and platform. Review every
   `read` and `propose` entry; no implicit all-document grant is needed.
3. Check all triggers are manual and there are no action declarations. A
   file-added trigger would allow later input edits to start chargeable runs.
4. Run the offline check against the draft and inspect its exit status:

   ```sh
   go run ./cmd/canvas --validate \
     --definition examples/northwind-released/canvas-draft.yaml
   ```

`--validate` requires `--definition`. It uses the strict generic definition
loader with a nil release resolver, then exits before runtime setup. It checks
the definition, declared schemas/seeds and local suite assets. Released suites
must include `suite.layout`; registry-only references refuse even if a runtime
cache already exists. It performs **no registry fetch/cache, provider setup or
model calls, runtime state creation, watcher, Sessions host or HTTP listener**.
Suite inspection may create temporary validation files, removed on return.
Runtime flags do not configure or qualify a runtime during this check.

A successful check means **valid offline; not activated**. It is not
authorization, a suite/model-quality evaluation, runtime compatibility proof,
deployment qualification or approval to execute. These released fixtures use
mock evaluation inputs and a mock model; compilation does not make them a
real-provider configuration.

Activation remains a separate, deliberate human decision after diff review,
runtime/model qualification and appropriate trust checks. The ordinary Canvas
command without `--validate` starts the runtime and may create state; it is not
a validation substitute. Use the [generic local Canvas guide](generic-canvas.md)
for its separate startup behavior and fresh-state rules. The existing
[Copilot review extension](copilot-review.md) is distinct from authoring;
do not generate a replacement or expose approve/continue/activate tools.
Prototype loopback capabilities and Copilot sign-in do not authenticate a
human-only reviewer. Keep this workflow local and fictional until reviewer
authority and any real-effect runtime have been independently qualified.
