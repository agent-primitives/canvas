---
name: canvas-authoring
description: Use when creating or editing a Canvas engine canvas.yaml package, document schemas, or seeds in Copilot. Not for GitHub Copilot canvas UI extensions.
---

# Canvas authoring

Draft setup files, validate offline, then hand the complete file diff to the
human. Copilot drafts configuration; it does not activate or approve Canvas.

## Draft a package

1. Locate the Canvas source checkout and read its
   [CLI contract](../../../docs/authoring-cli.md). Use the same shell/path
   environment as that checkout. Go 1.27 and dependencies must be available;
   first-time `go run` may download toolchain/modules, unlike offline validation.
2. Choose an unused directory inside the reviewable workspace, outside active
   watcher/input directories and runtime state. Preserve existing files and
   unrelated changes. Init refuses even an existing empty destination.
3. From the Canvas checkout root, use this Linux/WSL shell example:

   ```sh
   DRAFT="$(pwd -P)/review-draft"
   go run ./cmd/canvas init "$DRAFT" --fixture --template writer-review --json
   go run ./cmd/canvas validate "$DRAFT/canvas.yaml" --json
   ```

   Choose another unused name if needed; `single-review` provides one writer.
   Init copies complete pinned local assets. These are deterministic fixtures,
   not real-provider or evaluated user-agent configurations.
4. Edit only the requested setup files with normal file tools. In fixture mode,
   preserve generated IDs, paths, schema bytes, pins, scopes and manual triggers;
   metadata/display labels and valid seeds may change. A seed is
   `{"text":"Fictional draft for review."}`, with 1–4096 text characters.

## YAML rules

- Use `apiVersion: canvas.local/v1alpha2`, `kind: Canvas` and string metadata.
- `spec.documents` maps document IDs to paths/schemas. `spec.seed` maps document
  **paths** to seed-file paths. Schema, seed and layout paths are relative to the
  definition directory: no absolute paths, `..`, hidden components or symlinks.
- Agent `read`/`propose` scopes name declared document **IDs**, not file paths.
  Keep scopes narrow and explicit. Released suites need exact `ref`, local
  `layout`, `member` and `platform`; preserve reviewed pins and asset bytes.
- Keep triggers `type: manual`, bound to declared agents; omit actions and event
  triggers. For a different workflow, use the maintained
  [authoring reference](../../../docs/canvas-authoring.md), not fixture edits
  disguised as qualified execution. New agent/suite production belongs to kmx;
  do not invent digests or a `kmx canvas` command.

## Validate and hand off

After every edit, run `go run ./cmd/canvas validate "$DRAFT/canvas.yaml" --json`
from the Canvas checkout. Require exit 0, `ok: true` and `status: "valid-offline"`.
Fix invalid/refused drafts and revalidate; don't bypass errors with startup,
change fixture blobs/pins, or claim validation you could not run.

Show the complete setup-file changes in Copilot's native **Changes** review,
including new files and relevant schemas, seeds and assets, not just a chat
summary. If those files are not visible there, report native review unavailable;
show exact files/diffs without claiming approval. Do not stage, commit or apply
review decisions for the human.

Report changed paths, actual validation command/result, and the remaining human
review step. Native file-diff review is **not engine-recorded setup acceptance**;
valid offline is not authorization, model quality or deployment qualification.
Stop here: no runtime start, engine write routes, approval/continuation, secrets,
plugin/MCP installation or permission-setting changes. Instructions do not
sandbox normal tools; keep the host's existing permission prompts.

For discovery and a sample request, see the
[Copilot authoring guide](../../../docs/copilot-authoring.md).
