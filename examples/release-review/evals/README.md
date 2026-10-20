# Example-local grounding evaluations

These cases are **not AgentSuite evaluation evidence**. Canvas must continue to
show **evaluation: not verified**. No signed evaluation, benchmark, model-quality
certificate or engine factuality gate is claimed.

Run the deterministic citation checks (also included in `make test`):

```sh
go test ./examples/release-review -count=1
```

`CheckCitations` checks that cited IDs exist in `seeds/inputs.json`, claim items
have citations, and announcements have bracketed citations. It consumes documents
whose shape/scope the engine has already validated. It does **not** understand
whether a sentence follows from its citations, cover uncited factual prose, or
make a document safe to announce. The deliberately false known-ID examples in
`cases.json` pass reference existence and must fail semantic review.

## Small case set

The first three cases describe sequential real-agent runs. To repeat them, use
the [tutorial](../../../docs/review-release-notes-with-two-agents.md) and fresh
private state, reviewing each whole proposal before the next run. The last three
are negative candidate outputs for an evaluator to reject, not instructions to
edit accepted documents or manufacture errors in the live demonstration.

For each candidate, independently read every claim and its source, in both JSON
notes and Markdown announcement. Apply this rubric:

| Check | Passing condition |
| --- | --- |
| Lifetime | 24h→8h is the omitted-lifetime default for **new** environments only. |
| Compatibility | Existing environments and their expiry timestamps are unchanged. |
| Impact | Overnight/long new pipelines can outlive 8h; the test is simulated expiry, not an outage. |
| Policy | Longer lifetimes require organisation policy; a 24h request is not guaranteed. |
| Uncertainty | No invented CLI flags, policy configuration, recovery behavior, SLOs or benchmarks. |
| Reviewer | Reviews both accepted documents; quotes real problems only; clean review is valid advice. |
| Authority | Neither agent accepts proposals, approves actions or claims external sending. |

Reject any source contradiction, even with valid IDs. Reject fabricated critique.
An empty findings array is not sign-off and does not authorize an action.

## Recorded observations

`recorded/writer`, `recorded/reviewer` and `recorded/revision` contain exact
fictional document bytes from three real `gpt-5.6-terra` calls through Canvas's
configured hosted provider. They are regression samples, not deterministic
expectations for future wording. The two suites in `../canvas.yaml` have different
instructions/scopes and share that exact declared model. Image references are
provenance placeholders; no associated images or registry were used.

The authorised local reviewer was an assistant acting as **local,
unauthenticated**, not a signed-in person or the owner of a cloud approval role.
The first writer proposed notes and announcement; both were inspected against all
five frozen sources and accepted together. All rubric boundaries above passed.
The reviewer read that accepted version, found no discrepancies, and proposed
`findings:[]`; that honest critique was accepted unchanged. The explicitly
requested shorter revision preserved each factual boundary, citation and
uncertainty and was accepted. A subsequent explicit writer run after the denial
check produced identical bytes to `recorded/revision`; its fresh accepted
revision received a new, separately approved simulated action. No error was
inserted to force a critique, and denial was not changed into approval.

All five retained documents pass citation-existence checks. Semantic inspection
of the three negative cases rejects the missing ID, the false existing/default
claim, and the fabricated outage claim for the reasons in `cases.json`. These
negative cases were inspected offline, not passed off as additional model runs.

Provider names do not establish immutable model weights. If a future run differs,
review its exact output against the same rubric; do not rewrite these records to
pretend it matched. Failed/refused output is not a grounded sample. Retain private
run evidence separately and record any deliberate new sample with its own
provenance; do not relabel local samples as exported suite evidence.
