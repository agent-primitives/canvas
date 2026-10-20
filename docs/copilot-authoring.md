# Draft Canvas setup files in Copilot

The repository skill [canvas-authoring](../.github/skills/canvas-authoring/SKILL.md)
teaches the Canvas engine's YAML, local templates and offline validator. It does
not create a Copilot canvas UI extension; Copilot's built-in `/create-canvas` is a
different feature. There is no MCP server, plugin bundle, new tool or runtime.

## Use the repository skill

Open this Canvas checkout as your Copilot project. GitHub documents that
repository/CLI skills are also available in the Copilot app; its **Customize →
Skills** view manages skills. Ask Copilot to use `canvas-authoring` explicitly:

> Use the canvas-authoring skill to create a writer-review fixture package in a
> new review-draft directory. Draft a fictional release-note seed, validate it
> offline, and show all setup-file changes in native Changes for my review.
> Don't start Canvas, approve anything, install a plugin or change permissions.

The skill stays repository-scoped; this does not install it globally or modify
app settings. Keep existing tool-consent prompts. The examples use a Linux/WSL
shell with Go 1.27 and dependencies available. A Windows app backend is not
necessarily WSL: verify that its shell, checkout and paths refer to the same
environment before running commands. Don't guess path translations.

## Review the files, not the summary

After the agent edits files, choose **Changes** above the prompt box to view the
diff. Inspect the definition, schema, seeds and referenced assets, including
newly created files. Check exact suite pins, explicit document-ID
scopes and manual triggers. No setup-file review grants execution authority or
records Canvas engine acceptance. If the native review does not show all files,
stop and inspect exact files/diffs instead; do not pretend review occurred.

Require the actual validator's successful exit and `valid-offline` result after
edits. Invalid seed/schema/reference results must be fixed and checked again.
Validation does not establish model quality, authenticated reviewers or approval
to run. See the [CLI contract](authoring-cli.md) and
[authoring boundaries](canvas-authoring.md) for details.

Native Copilot app discovery and Changes behavior must be checked in the actual
app/environment; CLI checks and agent exercises alone do not qualify that UI.
For a **separate, deliberate** local runtime/review exercise after setup review,
follow [Your first review canvas](your-first-review-canvas.md).

GitHub references: [app customization](https://docs.github.com/en/copilot/how-tos/github-copilot-app/customize-github-copilot-app),
[agent skills](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/create-skills)
and [the native Changes workflow](https://docs.github.com/en/copilot/get-started/quickstart-copilot-app#making-your-first-code-changes).
