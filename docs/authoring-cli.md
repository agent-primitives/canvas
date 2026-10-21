# Initialize, bind and validate a Canvas package

`canvas init` writes a new, portable definition package. `canvas validate` checks
its definition and local dependencies without starting an engine. These commands
are the setup portion of [Your first review canvas](your-first-review-canvas.md).
That tutorial continues with explicit local fixture startup and a reviewed
proposal. A valid package alone is not the finished tutorial.

## Prerequisites and asset source

Use Go 1.27 and a Canvas source checkout. Init reads the reviewed released fixture
layouts under `examples/northwind-released/layouts/` in that checkout. It does not
fetch assets, build agents or embed OCI layouts in the binary. After init, the
output package contains complete copies of its dependencies and no longer needs
the checkout to validate. An installed `canvas` binary can use `--root` to select
the source checkout; commands below use `go run` from its root.

Both templates currently require **explicit `--fixture`**. Their suite identities
are known, deterministic example bindings, not evaluated user agents or qualified
real-provider configurations. No model is contacted by init or validate.

## Make a new package

The destination's parent must exist. Pick a destination that does not exist—even
an existing empty directory is refused. A package name is 1–63 lowercase letters,
digits or hyphens, starting with a letter. By default it is the destination folder
name; use `--name` when the folder name does not follow that rule.

```sh
go run ./cmd/canvas init /tmp/my-review --fixture --template single-review
```

The single-review template declares one writer and one JSON document. For a writer
and separate reviewer with two documents:

```sh
go run ./cmd/canvas init /tmp/team-review --fixture --template writer-review
```

Both templates use explicit `read` and `propose` scopes, manual triggers and no
actions. Several agents do not imply several authenticated human reviewers or a
quorum. Init never starts Canvas, creates runtime state or changes existing files.
If copying fails, init attempts to remove only its newly created destination;
inspect any remaining partial package before retrying.

For an installed binary, for example:

```sh
canvas init /tmp/my-review --fixture --root /path/to/canvas-checkout
```

`init DIR` resolves DIR relative to the current directory, **not `--root`**.
For init, `--root` selects source assets only. Portable output files use 0644 and
directories 0755; source checkout permissions are not changed.

## Edit and validate offline

Edit `seeds/draft.json`, keeping its JSON object with a nonempty `text` string (at
most 4096 characters). The writer-review package also has `seeds/review.json`.
The package includes its local schema, `canvas.yaml`, and full OCI layouts.

```sh
go run ./cmd/canvas validate /tmp/my-review/canvas.yaml
```

With an installed binary, this does not require a checkout:

```sh
canvas validate canvas.yaml --root /tmp/my-review --json
```

Validation uses the same strict loader as generic startup: known YAML fields,
document/agent/scope/trigger/action references, schema compilation, seed contents,
exact suite digests and member/platform selection. JSON Schema references must
stay inside their schema document (fragment references); schemas cannot fetch
other files or URLs. Released suites require `suite.layout` with complete local
bytes. No registry/cache resolution, provider configuration, inference, runtime
state, watcher, Sessions host or HTTP listener starts.

`validate FILE` resolves a relative FILE under `--root` (current directory by
default); an absolute FILE is unchanged. Sources declared inside the YAML are
relative to the definition's directory, not to `--root`. Keep schema, seed and
layout paths portable: no absolute paths, `..`, hidden components or symlinks.
The existing loader also refuses symlinked ancestors of the definition path;
choose a real directory path if a platform's temporary-directory alias is a link.
Moving only the YAML is not sufficient; move the entire generated package.

Validation success means **valid offline; not activated**. It does not establish
model quality, authorization, runtime compatibility, authenticated reviewer
identity or deployment qualification. Do not use ordinary startup as a validation
substitute. Existing `canvas --validate --definition FILE` remains available with
its original flag-only behavior.

## Bind a supplied suite without activation

`canvas bind` binds an **existing declared agent slot** to supplied local released
suite assets. It creates a new unused output directory; the source is unchanged.
It does not create an agent, run kmx/Lift/evals, activate, or start runtime state.
First place the complete reviewed OCI layout inside the draft package, then use
its actual exact reference, member and platform:

```sh
canvas bind /tmp/my-review/canvas.yaml --out /tmp/bound-review \
  --agent writer --ref "$REVIEWED_SUITE_REF" --layout layouts/custom-writer \
  --member "$REVIEWED_MEMBER" --platform "$REVIEWED_PLATFORM" --json
canvas validate /tmp/bound-review/canvas.yaml --json
```

The binding retains the slot's ID and read/propose scopes and leaves other agents
unchanged. A changed binding drops that slot's old evaluation association; it does
not fabricate replacement evidence. Missing assets, a wrong pin/member/platform,
unknown slot, unsupported profile or existing output refuse. Execution still needs
an explicitly supported provider; offline validity is not model qualification.
Capture is limited to 1,024 files, 4,096 visited entries (including directories),
8 MiB per file and 32 MiB total. Directory enumeration is incremental; empty
or repeated directories still consume the traversal budget.

## Compile declared flows (v1alpha3)

`canvas validate FILE --json` and `canvas --validate --definition FILE` accept
`canvas.local/v1alpha3`. Existing v1alpha2 definitions, manual triggers, templates
and saved definition digests remain unchanged. v1alpha2 rejects the new fields,
including empty ones. v1alpha3 can declare flows alongside existing manual and
file-added triggers; without a flow, a manual trigger is still required.

Flow support is **compilation only**, not a scheduler. Canvas, the standalone
Sessions host and the Copilot review command refuse flow-bearing definitions
before dependency resolution/cache writes or runtime startup. Validation does
not activate flows or enforce gates on a running agent.

For example, extend a generated single-review package with this fragment, keeping
its other agent, seed and schema declarations:

```yaml
apiVersion: canvas.local/v1alpha3
# Keep kind and metadata from the generated package.
spec:
  documents:
    draft:
      path: draft.json
      schema: schemas/document.json
      editAgent: writer
  reviewRoles: [reviewer]
  flows:
    review:
      inputs: {schema: schemas/run.json}
      limits: {agentRuns: 12}
      steps:
        - id: draft
          agent: writer
          gate: {accept: [draft], role: reviewer}
```

Add `schemas/run.json`, for example:

```json
{"type":"object","properties":{"topic":{"type":"string","minLength":1,"maxLength":256}},"required":["topic"],"additionalProperties":false}
```

Input schemas must explicitly declare an object root. They use the same local,
bounded, non-symlinked files and no-external-reference resolver as document
schemas; defaults must satisfy their schemas. Exact input-schema bytes enter the
definition digest. The compiled input validator checks strict JSON objects up to
64 KiB against its loaded snapshot, without inserting defaults or rereading files.
Input values and input-schema constraints that lose decimal precision in the
schema validator's numeric representation are refused rather than rounded.

Each flow has 1–32 ordered steps and a required positive integer `agentRuns`
budget. A gate has a declared role and 1–32 unique document IDs in that
agent's propose scope, never input/glob documents. The limit matches the maximum
replacements one proposal can contain. Gate documents are required
outputs for the future scheduler; compilation does not change manual proposal
validation. An optional action must be last, reference a declared action and use
a document required by an earlier gate. At least one gated agent step is required;
there are no jumps or optional gates. Definitions allow up to 128 flows and 32
unique named review roles. Roles are logical declarations, never authority grants.

An optional document `editAgent` must name an agent with both read and propose
access to that noninput document. Compilation only records the binding; it does
not edit documents or provide an edit tool. Budgets are authored per-flow defaults;
operator ceilings, per-run selection, counting retries/edits, and actual human
review/identity enforcement belong to runtime support, not offline validation.

## Run a generated package locally

After validation, explicitly choose the deterministic provider and a dedicated
state directory:

```sh
canvas --fixture --definition /path/to/my-review/canvas.yaml \
  --state /path/to/new-state --listen 127.0.0.1:0
```

The generated template structure, exact suite pins, schema bytes, scopes and
manual triggers must remain unchanged; metadata and valid seed edits work.
Fixture mode ignores ambient model configuration and refuses explicitly supplied
`--model-profile`, `--model-base`, `--key-file`, `--sessions-address`,
`--sessions-only` or `--validate`, including explicit default/false values.
The CLI and browser label the actual provider as deterministic; suite/model
identities remain authored metadata, not model execution proof.

A missing or empty state directory may start in fixture mode. Same-mode restart
preserves history. Opposite-mode or corrupt/linked `runtime-mode.json` refuses
without changing history. Existing unmarked state stays usable for ordinary
real/configured startup, which records its mode before host activation.
The marker binds this command's provider selection; older binaries and other
commands do not honor it, and it is not historical inference qualification or
OS isolation. See the tutorial for the complete review/stop/reopen sequence.

## Scriptable results and refusals

Add `--json` to any authoring command for exactly one JSON object on stdout. Version
`canvas.authoring/v1` has these fields:

- `schemaVersion`, `operation` (`init`, `bind` or `validate`), `ok`, `status`;
- `files`: sorted package-relative file inventory for successful init/bind, otherwise
  an empty array;
- `diagnostics`: zero or more `{code, path, pointer, message}` records. Paths and
  pointers may be empty when the strict loader cannot safely identify a field.
- `help`: present only for help requests.

Statuses are `created`, `valid-offline`, `invalid`, `refused`,
`usage-error` and `help`. `created` does not mean runnable or evaluated. Exit codes
are 0 for success/help, 1 for invalid/refused/input-output failure, and 2 for usage
errors in these new commands. Existing flag-only errors keep their prior exit
behavior. `--help --json` returns JSON help rather than mixed text and JSON.
Diagnostics on stderr, if any, are not another JSON result.

Use an operand followed by flags, as above, or put all flags before the operand.
Extra operands and runtime flags (for example `--sessions-address`, `--state` or
`--model-profile`) are not accepted by these commands.

On `invalid-definition`, check the referenced file paths, known fields, scopes,
schemas and seed values, then verify complete layout bytes and exact pins. The
loader intentionally returns a bounded generic refusal for many invalid inputs;
it does not echo file contents. Unsupported named runtime profiles produce
`unsupported-runtime-profile`. Init refusal points to a new destination or the
required source-checkout assets without exposing their contents. Do not alter
fixture blobs or pins just to get past validation.

For a user's own agent, kmx remains the agent/suite producer. A suite's OCI
manifest digest is distinct from its graph/member digests. Format validity does
not make an arbitrary kmx composition executable by Canvas; tools, named runtime
profiles, models and other execution restrictions remain. See
[the existing authoring boundaries](canvas-authoring.md) and
[generic local startup](generic-canvas.md) for current behavior. Copilot drafting,
setup-file approval/export, real-agent integration and Lift are not added here.
