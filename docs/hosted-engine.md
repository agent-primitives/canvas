# Standalone hosted Canvas engine

`canvas-engine -config FILE` is a headless, single-writer Canvas runtime for an
explicitly trusted private network. It connects to an existing **TLS Sessions
host**; it never starts an embedded host, loads provider configuration, accepts
model/identity credentials, or fetches suites from a registry. The Sessions host
owns model configuration, credentials, completion policy and journal storage.
This mode requires `authMode: "off"` and `trustedNetwork: true` explicitly.
**Identity is not exercised.** Role labels are attribution, not authorization.
Do not expose either unauthenticated service publicly or use it for tenants.

## Image and operator preparation

Build locally (no push is implied):

```sh
go build -o /tmp/canvas-engine ./cmd/canvas-engine
docker build -f deploy/Dockerfile --target engine -t canvas-engine:local .
```

The separate `engine` image runs as UID/GID **65532:65532** and contains only the
engine binary plus Alpine, Git and CA support—not the producer, Sessions host,
example scenarios, provider files or credentials. Existing `app` and `producer`
image entrypoints are unchanged. Image builds need their pinned base images and
normal build/package inputs available; a Go/source test is not an image test.

Prepare a disk-backed POSIX filesystem mount for the engine, separate from the
Sessions host journal. For Kubernetes use one retained Filesystem PVC per engine
instance, AzureDisk CSI/POSIX storage, preferably **ReadWriteOncePod**. AzureFiles /
SMB and shared active-active writers are not supported. Use one replica and
Recreate/no surge. RWO/PVC attachment and an advisory lock are not cross-node
fencing: fence an old node/writer before any force recovery or PVC reattachment.
Do not delete state, lock files or ledgers to force a restart.

Before running the image with a read-only root filesystem, the operator must:

- Supply `/data` as a prepared writable mount, with writable **`/data/home`**
  and **`/data/tmp`** for UID 65532. The image sets `HOME=/data/home` and
  `TMPDIR=/data/tmp`; it does not create or repair these mount children.
- Put the exact prepared definition at `/data/definition/canvas.yaml`, including
  its real local source/schema/seed files and AgentSuite artifact/release layouts.
  Preserve original content and source file modes (`umask 022` during checkout /
  preparation); suite identities include modes. No linked definition/source
  paths, registry resolution, automatic copying or scenario generation occurs.
  Released suites require a prepared local `layout`, not registry-only refs.
  Flows are rejected (`RequireManualRuntime`); only manual runtime is supported.
- Leave `/data/state` absent or empty and private. It is an engine-owned **0700
  child**, not the mount root. Existing state must already be 0700, without
  setgid/setuid/sticky bits or symlinks/special files. Nonprivate or read-only
  existing state is refused, not chmod/chowned or migrated. A new child under a
  setgid parent has setgid cleared on **that new child only**; parents are untouched.
- Mount readable TLS material separately. `/sessions-ca/ca.crt` contains only
  the trusted Sessions CA(s), **not the host's private key**. The engine gets its
  own `/engine-tls/tls.crt` and `/engine-tls/tls.key`. Keep Secret access limited
  to the runtime UID without weakening private file modes. Kubelet projected
  `..data` Secret symlinks are supported: opened targets must be regular files,
  at most 1 MiB each. TLS material is loaded once: **rotation requires restart**.

## Closed version-1 configuration

Every key is required, with these exact spellings and JSON types. Unknown keys,
case aliases, duplicates, nulls, trailing JSON, omitted values, and files over
64 KiB or nonregular opened targets fail startup. Paths must be absolute and
clean (no `..`, redundant separators or trailing slash). No environment/provider
credential discovery or credential fields exist.

Example production-shaped `/config/engine.json` (replace the instance, origin and
verified Service/namespace names):

```json
{
  "version": 1,
  "authMode": "off",
  "trustedNetwork": true,
  "instanceID": "review-release-one",
  "listen": "127.0.0.1:8443",
  "origin": "https://127.0.0.1:18443",
  "readinessAddress": "127.0.0.1:8081",
  "stateDirectory": "/data/state",
  "definitionFile": "/data/definition/canvas.yaml",
  "sessionsEndpoint": "agents-sessions.default.svc.cluster.local:8080",
  "sessionsCAFile": "/sessions-ca/ca.crt",
  "tlsCertFile": "/engine-tls/tls.crt",
  "tlsKeyFile": "/engine-tls/tls.key"
}
```

`instanceID` is a bounded project name (`[a-z][a-z0-9-]{0,62}`), independent of
the definition ID. Both the Sessions client default project and Canvas's runtime
project use it; the source definition's ID/digest are not rewritten. Use a unique
instance and private state/PVC per release.

The API and readiness bind **only literal 127.0.0.1**, never `0.0.0.0`, DNS or
IPv6. Ports must be numeric and separate. Port 0 is accepted only on these two
listeners for local ephemeral tests. The configured browser `origin` must be
canonical `https://127.0.0.1:<nonzero-port>`, without credentials, path, query or
fragment; it need not use the pod's API port.

`sessionsEndpoint` accepts only literal `127.0.0.1:<nonzero-port>` for local TLS
assembly, or exactly `<service>.<namespace>.svc.cluster.local:<nonzero-port>`.
No URLs, public endpoints, resolver schemes, proxies or plaintext downgrade are
accepted. Both TLS hops require TLS 1.2 or later. The engine uses explicit CA
roots and endpoint hostname verification for Sessions, with proxy and RPC retry
fallback disabled. The host certificate needs the **exact Service DNS SAN**, for
example `agents-sessions.default.svc.cluster.local`. The engine certificate needs
**IP SAN `127.0.0.1`** (a Common Name or DNS-only SAN is insufficient).

## Startup, state and recovery

Startup validates the prepared definition and existing private state read-only,
loads TLS, and reserves **both** sockets before state writes or serving an API.
It then performs a bounded **5-second read-only TLS ListSessions probe**, without
CreateSession, Exec, Resume, model or executor work. Only after this succeeds does
it create a private child if needed, acquire the stable `.producer.lock`, and
revalidate the state under that lock.

Before SharedSpace or Canvas is opened, it atomically publishes and fsyncs a
private `hosting.json` binding:

```json
{"version":1,"instanceID":"review-release-one","definitionDigest":"<compiled-digest>","sessionsEndpoint":"agents-sessions.default.svc.cluster.local:8080"}
```

All four values must match exactly on restart. An unmarked nonempty root is
refused; only the stable regular `.producer.lock` is ignored on a fresh root.
A matching marker with no Canvas/SharedSpace ledgers is a valid partial bootstrap
and reopens without deletion/reset. Existing Canvas definition/project/policy
bindings still apply; old local/unmarked history is never adopted as hosted.
The producer lock stays held until Canvas, SharedSpace and the Sessions client
have closed. Shutdown stops HTTP, cancels Canvas work and releases resources once;
HTTP drain has a two-second bound. Interrupted rows reopen as uncertain, without
automatic Exec/Resume, retries, model calls or input scanning.

**Endpoint binding is not backend/journal identity.** `hosting.json` cannot detect
an empty/replaced Sessions journal behind the same DNS name/address. Retain and
fence the host PVC too. Existing per-session UID, project, harness and owner
checks fail closed on missing/moved recovery evidence; there is no fallback host,
new Exec or reconstructed authority. Readiness does not attest journal continuity.
Never “repair” this by deleting the engine marker/ledger or creating substitute
sessions. Changes to instance/definition/endpoint require separately prepared
state, not adoption or reset of history.

## Browser adapter and API

Port-forward the engine pod's loopback API to the local port named by `origin`:
for the example, local **18443** to pod **8443**. There is no engine Service,
ingress, public/wildcard listener, or asset server. A trusted local adapter
bootstraps at `GET /` to receive the Secure, HttpOnly, SameSite=Strict CSRF cookie.
It must use the configured Host and HTTPS Origin even when those ports differ
from the internal listening port. Forwarded / X-Forwarded headers never confer
trust. The guard is local CSRF protection, not user authentication or protection
from another trusted process/operator in the same network namespace.

The Copilot adapter's separate bounded connection file uses only
`version`, `engineURL` and `caFile`, selected by **path** in
`CANVAS_ENGINE_CONFIG`. Do not give the adapter the engine's runtime configuration.
For example, with a locally prepared CA path:

```json
{"version":1,"engineURL":"https://127.0.0.1:18443","caFile":"/absolute/local/engine-ca.crt"}
```

The iframe receives neither engine TLS keys, CA files, model/Sessions credentials
nor the engine cookie. Reopen the adapter after an engine restart: each boot has
a new `boot` value and CSRF cookie. TLS Secret rotation also requires an engine
restart and adapter reopening. No browser sign-in or identity proof is implied.

The runtime's exact surface is:

- `GET /`: text/bootstrap cookie only; no UI assets.
- `GET /api/demo`: `mode: "hosted-sessions"`, fresh `boot`, identity not exercised,
  `headlessPrefix: "/engine"`; `modelCalls` and `executorCalls` are **null/unknown**.
- `GET /engine/api/{state,document,forks,announcement}`: generic read views.
- `POST /engine/api/{run,review}`: explicit producer run and manual proposal review.
- `POST /engine/api/verify`: remote diagnostics unavailable, truthful **409**.
- Every `POST /api/announcement` or descendant and
  `POST /engine/api/announcement` or descendant returns **409 JSON** before any
  Canvas/Sessions action. There is no simulated hosted announcement success.
- Other root or `/engine` legacy mutations (`resume`, `ask`, `input`, `comment`,
  `role`, `regenerate`, etc.) are not mounted (404/405), even with a valid cookie.

Remote announcement execution, approval/replay and operator reconstruction are
not implemented by this runtime; they remain a later executor task. A successful
proposal/manual acceptance proves neither identity nor executor/model quality.

## Readiness

Use the binary's owned probe rather than a TCP probe:

```sh
canvas-engine health-probe 127.0.0.1:8081
```

It requires `GET /readyz` HTTP 200 with the exact owned response body within a
one-second deadline (below a four-second chart timeout), with no proxy or redirect.
The HTTP health server starts only after state/API initialization. Reserved but
unserved listeners can complete TCP handshakes during startup; **TCP connection
success is not readiness**. Readiness proves process initialization and the
startup read-only host TLS check, not live model calls, replay, identity, current
host availability, cross-node fencing or deployment qualification. No cloud/live
model/AKS qualification is claimed by local deterministic TLS integration tests.
