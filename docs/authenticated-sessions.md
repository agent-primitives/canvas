# Authenticated sessions through the Canvas engine

This opt-in path keeps the Copilot adapter on HTTP and routes document review and
simulated-action decisions through Canvas. **Only the engine holds the sessions
connection and separate human/service bearer credentials.** The normal
deterministic Northwind demo remains unauthenticated.

## Local development configuration

**The CLI cannot yet supply a verified execution-service binding.** The current
Entra verifier is user-only: `-sessions-config` describes the human credential,
not an engine service credential. The command below intentionally refuses until
a trusted Go host adapter supplies a separate `ExecutionService`; it is not a
qualified hosted startup recipe. Use the unauthenticated local demo for now.

The human configuration shape is retained for that future assembly. It requires
a separate private state directory and a normal checkout created under
`umask 022` (suite inventories include file modes):

```sh
go run ./cmd/copilot-review -state .state/authenticated-review \
  -sessions-config /absolute/path/to/sessions.json
```

An optional `-definition` selects a multi-agent, multi-document definition, as in
the local demo. Config paths below are absolute; relative paths resolve against
the engine's working directory, not the config file.

```json
{
  "endpoint": "127.0.0.1:18080",
  "caFile": "/absolute/path/to/sessions-ca.pem",
  "serverName": "sessions.example.internal",
  "tokenSource": { "file": "/private/path/to/access-token.json" },
  "identity": {
    "tenantID": "11111111-1111-1111-1111-111111111111",
    "audience": "22222222-2222-2222-2222-222222222222",
    "allowedClientIDs": ["33333333-3333-3333-3333-333333333333"],
    "requiredScope": "sessions.access"
  },
  "reviewPolicy": {
    "role": "GM",
    "reviewers": [{
      "issuer": "https://login.microsoftonline.com/11111111-1111-1111-1111-111111111111/v2.0",
      "subject": "<reviewer's-verified-object-id>"
    }]
  }
}
```

Replace the example IDs and DNS name with the tenant/app/certificate values.
The `-sessions-config` transport always uses TLS 1.2 or later, the configured CA
and normal hostname verification. It has no plaintext or skip-verification option.
A loopback port-forward still needs a certificate valid for `serverName`.
The separate [trusted-loopback option](producer-harness.md) is mutually exclusive,
explicitly unauthenticated and never a fallback for this configuration.

The file source must be a current-user-owned regular file with no group/other
permissions (`0600`), not a symlink. An external credential helper writes this
JSON envelope; **never put its real contents into source control or command-line
arguments**:

```json
{"accessToken":"<delegated-access-token>","expiresAt":"2026-10-19T12:00:00Z"}
```

Alternatively replace `tokenSource` with a command:

```json
{"command":["/absolute/path/to/credential-helper","--format","canvas-json"]}
```

The helper emits the same envelope on stdout. Arguments must contain only
non-secret options, never a token. The engine executes this argv directly,
without a shell; stdin and stderr are discarded, stdout is bounded to 64 KiB,
and the helper has a three-second deadline. Helper failures are reported with
fixed messages, not command output or token-parser diagnostics. A helper may
read an existing credential cache; interactive sign-in is not implemented here.
The engine never writes a token file itself.

The engine checks the signature against the configured tenant's HTTPS discovery
and JWKS endpoints, along with issuer, audience, tenant, delegated client, scope,
v2.0 token version, validity window and user token type. App-only credentials are
not accepted. Subject defaults to the verified `oid`; `subjectClaim: "sub"` is
available only when deliberately choosing that identity mapping. Use the same
mapping and trust configuration as the sessions host. These checks mirror the
host's `entraauth` delegated-token rules. Successful verification is not a
role grant: Canvas's document-review policy and sessions' tool-approval policy
are separate authorization boundaries.

Tokens are cached **only in memory**, renewed 30 seconds before expiry, and never
sent after expiry. A failed renewal refuses the action rather than using a stale
credential. One action pins the exact credential used to derive its identity;
queued execution refreshes only the configured service identity. Requester
identity is captured as attribution, not retained human credentials or the
completed HTTP request's lifetime. A changed service identity is refused instead
of silently switching owners. Human Approve and Resume use the original verified
request context; authenticated Resume is synchronous, not a background attempt
to reacquire a human token. Nothing automatically retries decisions or resumes.

## Review identity and errors

Authenticated mode serves only the generic `/engine/api/` action surface behind
the existing loopback Host/Origin/CSRF boundary. Legacy demo mutation routes are
not registered. The extension keeps its existing separate iframe capability and
server-held engine cookie. Neither is a human identity, and the adapter never
forwards browser Authorization headers or contacts sessions itself.

Document proposal acceptance/rejection stores `{issuer, subject, role}` at the
SharedSpace decision boundary. Accepted identity is also retained in its Git
checkpoint; rejected identity is retained in the private review ledger. Tool-call
approval is a separate decision beneath document review, not a replacement for it.
Its native identity uses the verified issuer/subject, while role remains separate,
selected Canvas attribution (labelled as such, not an authorized sessions role). Distinct reviewers can use the same role without collapsing
into a single `demo:<role>` principal. Roles do **not** grant sessions permissions.

Authenticated document acceptance **and rejection** require the configured
`reviewPolicy` membership and exact role. A verified but unlisted user, an
unentitled role, or a missing policy is denied before any review mutation. V1
supports one configured workspace role (`GM`, `coach` or `owner`) and an explicit
list of reviewer issuer/subject pairs. The engine assembles the proposal ID,
action and full changed-path set from stored evidence and passes them to its
review-policy interface. A later per-document policy can check those paths without
changing the frontend or trusting paths supplied by a viewer. Per-document rules
are not implemented here. The frontend uses the engine's configured role as a hint;
the engine still checks it on every review.

The review panel labels returned decision identity **engine-verified, not
sessions-attested**. Today's sessions response echoes caller-supplied provenance;
it does not independently attest that provenance. Canvas shows a tool reviewer
only after native decision evidence matches the engine's recorded identity and
decision, never merely because an approval intent was saved.

- `PERMISSION_DENIED` becomes HTTP 403: **You're not an approver for this session.**
- A document membership/role refusal also returns HTTP 403 with
  `PERMISSION_DENIED`, but says **You're not authorized to review these documents
  in this role.** It is not described as a sessions allowlist failure.
- `UNAUTHENTICATED` becomes HTTP 401: **Sign in again: sessions credentials are
  missing, invalid or expired.** A changed credential identity is called out.
- An auth-rejected approval clears its newly saved intent, so another reviewer
  can explicitly inspect and decide. A lost response/timeout remains uncertain.

Tokens never enter Canvas ledgers, Git documents, the session journal, API JSON
or the iframe. Only verified identity and role are durable. Upstream private error
text is not returned. Provider usage is unknown for the remote host, not zero.

## What is and is not qualified

Tests use an in-process sessions service with real TLS, sessions authn interceptors
and generated RS256 delegated human tokens in the same shape as `entraauth`'s
offline fixtures. A separate opaque fixture service credential is verified by a
test-only adapter. A test-administered owner table records the actual verified
CreateSession caller, independently of `Session.Identity`. Human grants bind an
issuer/subject, exact session and individual Approve or Resume method. These are
**fake host contracts**, not upstream production authorization, `cmd/host`, AKS
or a live Entra tenant. Tests cover separate requester/acceptor/owner provenance,
missing/revoked/wrong-scope grants, service self-approval refusal, expired
credentials, TLS/CA/hostname refusal, refresh and secret confinement.

The retained human configuration has **one token source per engine process**;
that source is never substituted for the execution service. Simultaneous
per-browser sign-in/source selection is not implemented. A cached token-file
replacement is not an immediate account switch. This loopback path is not a
shared multi-user deployment or isolation from other trusted local processes.

Prerequisites for a future hosted qualification (not a runnable procedure):

1. Register the API audience/scope and delegated client in the test tenant; obtain
   consent and the correct delegated user token. Grant the intended document
   reviewers membership in the engine's `reviewPolicy`. Qualify a separate service
   credential provider and external session/method-scoped human decision grants;
   neither ownership nor an engine role supplies those grants. Build interactive
   human acquisition against the token-source seam later; Copilot app sign-in
   alone does not supply this credential.
2. Provision the host certificate/CA and private access path. Keep the health
   listener separate; do not expose the host publicly while upstream per-RPC
   authorization/session isolation work is pending.
3. Register the exact Canvas definition's digest-qualified harness inventory on
   the host. The existing ad-hoc remote agent/harness is not automatically a
   Canvas document producer. The generic [inventory preparation/server command](producer-harness.md)
   exports the definition's immutable profile-4 archives for this purpose.
   Canvas's local archive resolver is **not** a remote
   contract distribution mechanism; historical provenance requiring that archive
   remains unavailable until a remote resolver is supplied.
4. The remote assembly does not expose Canvas's replay Diagnostics service.
   Both authenticated and explicit trusted-loopback modes disable replay controls
   and refuse these calls explicitly,
   without opening the old plaintext diagnostics connection or claiming replay 0/0.
5. Qualify the Copilot app/WSL path and live key rotation/acquisition separately.
   No cloud, tenant or role changes are made by this feature.

## Dependency compatibility

Both sessions and its Substrate integration are pinned to upstream commit
`f18a2cb1454d`. The provider adapter applies recorded schema and generation hints
at the HTTP boundary without changing the journaled request or its replay hashes.
This affects all Canvas commands, not just tests. Opening an existing session journal migrates
it to schema 5; the migration test checks that old event bytes/hashes and completed
zero-call reconstruction remain unchanged. Back up state first; rollback to the
old sessions binary against a migrated journal is not qualified. The existing
Substrate adapter tests are adjusted to the matching `ActorInfo` API, without
changing Canvas's runtime behavior.

## Execution ownership and human decisions (local fake contract only)

An authenticated engine separates authority:

- **Execution owner.** A Canvas engine service identity creates, executes, reads and replays sessions. It must come from a separately verified service credential injected by a trusted Go host adapter. The engine never relabels or reuses the human credential as the owner.
- **Human decisions.** Approve and Resume (Continue) always use the acting reviewer's own verified credential. Approval alone does not continue the session or produce a receipt; Continue is a separate, separately authorized human action.
- **Attribution.** Runs and proposals record `executionOwner`, `requestedBy` (verified requester), and, for automatic follow-on work, `triggeredBy`/`triggeringReview`. The legacy run `reviewer` field is a historical alias for the requester, never the owner. Browser or model JSON cannot supply these fields.

`-sessions-config` alone does **not** provide a service binding. The current Entra verifier accepts delegated user tokens only, so an authenticated engine started from the command line refuses before opening the workspace or contacting sessions with: "authenticated execution requires a separately verified service binding; hosted service credentials are not yet qualified". Hosted service (app) credentials, grant administration and hosted qualification are a separate follow-on; nothing here claims they work.

What is exercised today is a local demonstration and a fake-contract test fixture: an opaque fixture service token, real human JWT verification, and a test-administered owner/grant table keyed by issuer, subject, session and method. The pinned sessions SDK has no production session-owner or per-session grant API; `Session.Identity` is provenance, not authorization.

Once service-owned work is journaled, its service identity is bound to the
instance; rotating that identity requires new state. Legacy ownerless or
human-owned sessions remain readable history, not executable sessions to adopt.
A fresh service-owned document attempt may use the current accepted documents
without rewriting the old history. Legacy action snapshots remain inspectable
without a Sessions request, but cannot be continued or silently replaced for the
same accepted proposal: the old effect's outcome may be unknown. Review a new
proposal before requesting a new action; no old approvals are inherited.
