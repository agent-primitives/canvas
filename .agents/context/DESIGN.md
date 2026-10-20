# Canvas Design Context

## Scope and register

Product UI: a local-first, definition-driven document workspace, built with native HTML, CSS and JavaScript. Backend meaning is authoritative. This document covers the existing UI; backend contracts remain authoritative.

## Approved visual direction

Use a quiet navy header, cool pale canvas, generous decision paper and warm pending banner. The direction is proposal-first, with complete affected-document scope and exact proposed replacements. Do not return to dense toolbars, technical identifier chips or equal-weight panels.

A person reviews a concrete recorded recommendation at a desk or on a phone, in normal daylight. The interface is explicitly light-only. A genuine dark palette remains separate future work; a dark OS preference must not create mixed-theme controls or unreviewed surfaces.

## Hierarchy and layout

- The decision occupies about 60% of the default desktop room; conversation occupies 40% and is visually quieter.
- Recorded review state, whole-proposal scope, base/proposed document bytes and approval actions form the reading sequence.
- Let reviewers inspect every affected replacement while preserving the original immutable proposal target.
- Never infer document relationships, generated exports or evidence from filenames. Missing evidence stays missing.
- Keep approval actions available without metadata covering the real document. Phone content follows normal flow with a static header, readable tables and no horizontal page overflow.
- Navigation and Evidence toggle independently; local preferences belong to the selected persona. Pending work remains outside both panels.
- Put the pending action in a pale warm banner. Name the real next action without inventing authorization or an approval owner. Hide its Review proposal action when that same proposal is already visible.
- The room composer is one line until focused. Preserve drafts and distinguish room comments from recorded, read-only agent questions.

## Entry points and progressive disclosure

- Header **New…**: New agent run and Add source file for the declared definition.
- Proposal **…**: Try again; promote it beside a stale explanation when appropriate.
- **Evidence**: immutable identity, source revisions, model, duration, execution chronology, full document bytes, input differences and saved replay results.
- **Replay verified** opens an actual saved proof and history without a POST or model call. If no current verified proof exists, offer verification instead. Questions invalidate old proof claims immediately.
- Whole-proposal scope remains visible before accepting, with routes to every affected document. Scope is derived from the record, never assumed from a filename or agent.
- Keep at most one short line beside an action. Longer explanations belong behind help/disclosure or in Evidence. Cut repeated helper copy, not actual evidence or uncertainty.
- Use plain conversation labels such as “Writer · answering a question.” Do not use technical categories such as “Read-only answer · Saved excerpt.”

## Typography and spacing

Native system sans-serif; native monospace for technical evidence only. No external font, asset or framework dependency.

Body uses the browser's default root size (normally 16px). Intentional scale: 28px page title, 24px recommendation, 20px section, 16px body and 14px secondary text, expressed in rem. Line height is generous; body lines stay within roughly 72 characters. Spacing follows a 4px family (4, 8, 16, 24, 32, 48), with larger gaps between sections than between associated labels and values.

## Color and surfaces

Use tinted OKLCH neutrals and one blue accent. Warm amber and muted success/error tones communicate actual state rather than decorating speakers or sessions. The header is navy; the page is pale cool gray; the decision paper is near-white. No gradients, decorative side stripes, nested cards or hero metric tiles.

Tokens live in `web/style.css`: `--ink`, `--muted`, `--surface`, `--canvas`, `--line`, `--control-border`, `--blue`, `--blue-soft`, state colors and spacing variables. Controls have explicit light backgrounds. Focus on the header uses a high-contrast light outline; elsewhere use the blue outline. Borders and text require fresh contrast checks after palette edits, not inherited claims from a previous design.

## States and trust

Show one prominent meaningful review state per item: Awaiting your approval, Accepted, Rejected or Stale. Active/failed states come from recorded execution. Finished is secondary execution evidence, never approval. Replay verification is an integrity result, never human consent.

Keep hashes, IDs, revisions, bases and attempt identifiers out of the default view. Full identities remain accessible in Evidence. Accept/reject still targets the immutable proposal and full scope, and stale/CAS behavior stays backend-owned. Retry creates a new record, not an in-place edit.

Keep model-authored text inert. Do not render raw HTML, external resources or invented chain-of-thought. Bounded previews retain exact original wording; complete bytes remain accessible. Role changes record attribution, not sign-in or authorization.

## Accessibility and interaction

- Named icon controls, visible keyboard focus and touch targets at least 44px where practical.
- Keyboard close returns to a visible launcher, not an off-viewport element. Polling must not steal focus from a newer user choice.
- Safe same-owner metadata refresh preserves reading position. New head, review state, execution error/status, pending question or proof authority must clear obsolete claims immediately.
- Failed document retry keeps the recovery action focused and announces a concise error.
- Use concise polite status announcements, not raw diagnostic details. Skip initial history and duration-only polling noise.
- Test 320px and 390px phones, short desktops, doubled text and reduced motion. Automated accessibility results are supporting evidence, not certification.

## Anti-patterns

- Dense default proposal/session/base/revision chips or a row of every document tab.
- Duplicate status badges competing with the human review state.
- Equal-weight decision and conversation, permanently expanded composers or repeated instructional paragraphs.
- Collapsing away pending work, actionable controls or complete affected scope.
- Describing a prototype or deterministic fixture as live inference proof.
- Real logos, medical claims, league data or invented backend/authorization states.
