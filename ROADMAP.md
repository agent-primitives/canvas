# Canvas roadmap

Review durable agents’ proposals in Copilot, growing into shared document work.

## What people can do

1. **Try review today — available prototype.** Inspect fictional document changes,
   accept or reject them, then approve and continue a simulated action.
   Check the recorded run.
2. **Review real work on AKS — Preview 0, in progress.** Sign in, review,
   approve, restart safely and recover the record. Needs Sessions releases,
   aks-durable-agents and live identity/model/backup tests.
   Generic startup and document review now work locally.
   Earlier AKS proof was simulated and unauthenticated; label absent sandbox isolation.
3. **Bring deployed agents — planned.** Review kmx-packaged agents.
   Needs released Sessions Lift support, matching images and evaluation evidence.
4. **Work together — planned.** Several agents propose; reviewers decide under
   shared roles and rules. Needs authenticated membership and safe concurrent review.

## After the first public release

**Copilot authoring plugin — planned, not started:** create, validate, preview
and review a canvas from the app.

After Copilot, in order (**planned, not started**):
1. **Claude Code and Codex:** author with the same canvas CLI and instructions file.
2. **Claude Desktop with MCP Apps:** embedded review using the MCP server planned for Copilot.
3. **Claude Artifacts and any Codex UI:** later front ends, subject to qualification.

## Fixed boundaries

One engine; documents first; separate acceptance, action approval and continuation.
The engine enforces document-review roles: verified identity alone grants no review rights.
Decisions carry identity and role under multiple-reviewer policy.
Single-agent use is configuration; authoring stays scriptable and frontend-neutral.
