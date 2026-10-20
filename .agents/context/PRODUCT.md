# Product

## Register

product

## Users

- A document reviewer who accepts or rejects complete agent proposals in a fictional reference workspace.
- A compliance reviewer checking the decision record later.
- An engineer watching the local demo and evaluating how the human gate and recorded history work.

## Product Purpose

A shared room where people and agents work on documents. Agents propose, people decide, and every recorded agent decision can be replayed and checked. The shared decision record is the thread of truth; the approval surface is the human gate. This is not an IDE or a workflow builder.

## Brand Personality

Calm, exact, trustworthy. Dense where it helps a decision, quiet everywhere else. Someone reviewing a proposed document change on a laptop needs to distinguish evidence, pending approval and accepted changes at a glance, even in a brightly lit demo room.

## Anti-references

Chat-app clones, IDE layouts, dashboards with KPI tiles, and anything that makes a suggestion look like an approval.

## Design Principles

1. Center the decision and room conversation. Navigation and evidence are independently collapsible; remember layout locally per persona with a safe fallback.
2. Keep pending work visible outside collapsed panels. Use only recorded requests, questions and proposal states the backend exposes.
3. Show one prominent review state per item. A finished run does not mean accepted documents changed. Accept/reject acts on the visible proposal; stale proposals explain why acceptance is blocked. Keep technical IDs, revisions, execution details and replay proof in Evidence.
4. Preserve traceability through progressive disclosure. Every relevant recorded item keeps a one-click route to evidence; replay verification exposes the integrity result and recorded history.
5. Make action consequences exact in one short line. Longer help belongs behind a question mark or in Evidence. Trying again produces a new recorded attempt and proposal when successful. Asking why uses recorded evidence and never changes documents.
6. Give the decision about 60% of the room and the conversation 40%. Use a quiet header, pale canvas and spacious decision paper. A New… menu owns explicit agent-run and declared source-file entry; the proposal menu owns Try again. The conversation composer stays one line until focused.

## Accessibility & Inclusion

Support keyboard navigation, visible focus, readable contrast, phone-width layouts and reduced-motion preferences. Audit against WCAG AA without claiming conformance before verification. The page is explicitly light-only, with light color-scheme and explicit surface backgrounds so dark OS preference cannot partially recolor it. Genuine dark-theme support is separate future work.

Only fictional data is used. The persona switcher is role-play and attribution, not sign-in or authorization. No external fonts, assets or new JavaScript framework.
