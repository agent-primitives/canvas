# Durable simulated-announcement executor

The optional internal executor records **local simulated receipts only**. Nothing
is sent or published. The native Sessions controller still owns approval and its
execution/call/decision bindings; the snapshot, a display role, or this database
cannot authorize a tool call.

## Assembly and compatibility

`internal/announcementexec.Open` opens a separate private state directory. Supply
its `Execute` method as `sessionhost.Options.AnnouncementExecutor` for an explicitly
opted-in **local** Sessions host. The callback is borrowed: keep its Store open
until after `Host.Close`, then close the Store. A nonnil `BackendFactory` cannot
be combined with this local callback. `NewAnnouncementHarness` exposes the same
fixed model-free harness descriptor; no models or tools are added to agent suites.

Omitting the callback preserves the existing local executor, state and wire
receipts. Old receipts are not adopted into a new counter database. This is an
internal assembly seam, not a remote executor, CLI, chart option, hosted capability
or operator reconstruction command. Those require separate implementation.

## Durable state and execution

The state directory must be private POSIX storage (0700), with regular 0600 files.
One process holds a stable lifecycle flock; SQLite uses DELETE journals and FULL
synchronous commits. Existing unsafe permissions, symlinks/special files,
unrecognized contents, empty/corrupt databases, incompatible schemas and hot
journals are refused, not repaired or reset. A new store gets a persistent random
store ID; that is provenance, not backend-journal continuity or node fencing.

Every admitted invocation is committed **before** outcome deduplication. The key
is native session UID plus operation. The first invocation binds the exact
canonical snapshot. Matching re-entry records another invocation and returns the
same receipt. Different bytes under that key record the conflicting invocation
but cannot change the binding or create another outcome. Invalid native context,
route or snapshot is refused before admission.

Approve alone invokes no executor. Explicit Continue can commit one outcome.
A lost native result after a committed receipt can be reconciled by explicit
native recovery without creating another receipt. Such recovery is not replay.

## Cancellation and uncertainty

During BEGIN or the transaction body, an ended executor/run context is returned
as its context error, unless storage corruption is detected and fences the Store.
Admission and outcome COMMIT are independently bounded and shielded from caller
cancellation. Only definite SQLite BUSY can retry the same BEGIN/COMMIT once;
this is lock contention handling, not an automatic model/tool/effect retry.

After a failed COMMIT acknowledgement, the old connection is terminated and the
same store identity and exact bytes are checked through a fresh read-only
connection. A known committed receipt is returned unchanged; proven absence is
an error, never success. If its presence cannot be determined, `ErrUncertain`
fences further calls on that Store and does not wrap a context error. Detected
storage corruption also fences the Store, including other session keys.

The SDK can record terminal `EXECUTOR_ERROR` even when storage has a receipt.
That cross-ledger state is **uncertain**, not pending approval or proof that no
simulation occurred. Preserve both stores and inspect them; do not reset state,
automatically request another operation, or advertise a successful native result.
Reopening validated storage does not manufacture missing native journal evidence.

## Read-only evidence

`ReadEvidence` reports version, store ID, admitted-invocation count, outcome count
and a canonical ordered-row SHA-256 digest. A one-second read-only transaction
takes no writer lifecycle flock and creates/initializes/repairs nothing. Busy,
missing, hot or inconsistent state is unavailable evidence, not zero. Short
readers can coexist with a writer; bounded contention does not by itself poison
execution.

These facts do not prove approval, external counter invariance, native journal
continuity, backup/restore, identity or execution replay. A later operator proof
must separately capture and compare real model/executor evidence around actual
isolated `controller.Replay`. Reading the Sessions Replay RPC alone is not that
reconstruction.
