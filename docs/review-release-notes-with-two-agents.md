# Review release notes with two agents

Use two real, separately instructed agents to draft and challenge fictional
**envctl 0.9** release notes. A local reviewer accepts whole proposals, then
separately approves a receipt-only announcement simulation. Allow about 20 minutes
after model setup; provider latency and wording vary.

This is tutorial 2. Unlike [tutorial 1](your-first-review-canvas.md), it requires a
real hosted model. Everything stays in local Canvas/Sessions state except model
requests to your configured provider. There is no live GitHub, registry push,
file export or announcement send. Local reviewer attribution is **local,
unauthenticated**, not sign-in, verified membership or a cloud approval role.

## 1. Configure the exact declared model

Use a Canvas source checkout with Go 1.27+, Python 3.10+ and a browser. Follow
[hosted model setup](local-models.md#using-a-hosted-model-instead) to create a
private key file and Git-ignored `model.config.local.json`. Both checked-in suites
declare **`gpt-5.6-terra`**; your endpoint must serve that exact model/deployment
name, and `hosted.model` must match it. Use your own endpoint and private key, not
fictional credentials. Model requests may incur charges.

```sh
make check-model MODEL_PROFILE=hosted
```

This checks metadata reachability, not inference availability or immutable model
weights. The default offline `qwen3:8b` setup does **not** match these suites.
`recorded model differs from selected profile` means stop and fix that mismatch;
there is no fallback or runtime override of a released suite's model. If you
cannot access the declared model, re-author and review new matching suites/layouts
with the suite tooling before a new run; do not just edit a digest or model pin.

## 2. Inspect and validate the package

Open [the frozen inputs](../examples/release-review/seeds/inputs.json). The launch
brief promises “seamless upgrade, no action required.” The fictional PR/test/policy
excerpts establish something narrower:

- New environments with no explicit lifetime default to 8h instead of 24h.
- Existing environments and their expiry timestamps are unchanged.
- A simulated 12h overnight pipeline loses its new default-lifetime environment.
- Longer lifetimes require organisation-policy permission, not a guaranteed override.

Would you tell overnight-pipeline users no action is required?

```sh
go run ./cmd/canvas validate examples/release-review/canvas.yaml --json
```

Require `ok: true`, `status: "valid-offline"`. This validates schemas, seeds,
scopes and complete digest-pinned local layouts, not grounding or authorization.
The writer can propose notes/announcement; the reviewer can propose only review.
Neither can propose changes to the inputs. Both read accepted documents, not each
other's pending edits. Both triggers are manual.

## 3. Start deliberately and accept a grounded draft

Choose an unused state path; keep it private and Git-ignored. From the checkout:

```sh
go run ./cmd/canvas --definition examples/release-review/canvas.yaml \
  --state .state/release-review-first --model-profile hosted --listen 127.0.0.1:0
```

Open the printed **Canvas:** URL. Nothing runs merely because the page opens.
Do not pass `--fixture`: these are real-model suites, not fixture templates.

1. Choose **New… → New agent run → Release notes writer**.
2. Request: “Draft grounded envctl 0.9 release notes and announcement from the
   frozen sources. Preserve compatibility boundaries and policy caveats.”
3. Wait for a proposal. A refused/failed run is not a successful draft; inspect
   the recorded result rather than automatically retrying or weakening validation.
4. Inspect **both** complete replacements in Document review against the source
   IDs. Use the [grounding rubric](../examples/release-review/evals/README.md):
   IDs must exist **and** support the actual claims. Unknown CLI syntax, policy
   configuration and recovery behavior must remain unknown.
5. Accept the **whole proposal** only if grounded; otherwise reject it. Acceptance
   changes accepted documents. It does not approve an announcement action.

The seed says “not drafted/reviewed.” A schema-valid seed or fluent proposal is
not release readiness. Local evals do not change **evaluation: not verified** in
Suite identity; that label concerns a different, absent suite-evidence format.

## 4. Exercise denial on this first accepted draft

Before revising, use **Local publication** to select the accepted writer proposal
and **Request approval**. Inspect the exact pinned Markdown, revision and digest.
Check **I inspected this exact snapshot…**, then **Record denial**. Reinspect,
confirm again, and **Continue simulation** to conclude the denial.

The result must stay **denied**, with no `SIMULATED_ANNOUNCEMENT` receipt. Repeating
Request approval for that same action/proposal selects the existing operation; it
does not undo denial. Do not try to turn it into approval. A different accepted
proposal gets a new operation and must be inspected and approved separately.

## 5. Critique accepted work, then explicitly revise

Request **Compatibility reviewer**: “Review the accepted notes AND announcement
against the frozen sources. Report genuine discrepancies only; do not manufacture
an error if the warnings are already correct.”

Inspect and accept or reject the complete review proposal. A correct draft may
receive `findings: []`; that is honest advice, not mandatory sign-off. Do not
insert an error to make the reviewer interesting. The reviewer cannot edit notes,
accept its own review or approve publication.

After accepting useful critique, explicitly request the writer again: “Read the
accepted critique. Revise both documents for concision, preserving the warnings,
policy conditions, unknowns and supporting citations. Do not invent a correction
if the critique found none.” Inspect both replacements and accept the whole
revised proposal if grounded. Nothing automatically hands work between agents.

## 6. Restart a pending gate, then approve without executing

Request approval for the **new accepted writer proposal**, not the denied one.
Inspect its exact bytes. Before deciding, exercise the mismatched-confirmation
check in section 7: it must return 409 and leave the gate pending.

While this request is **pending-approval**, stop Canvas with Ctrl+C. Restart the
same command, definition and state path. Open the newly printed URL and inspect
the same operation. It must still be pending with the same pinned text/revision/
digest. Restart is not approval, continuation or a new model run.

Check the inspection confirmation and **Record approval**. The operation should
now be **approved, not executed**, with no receipt. Inspect again and make a fresh
confirmation; only then choose **Continue simulation**.

Success is one journaled **`SIMULATED_ANNOUNCEMENT`** receipt containing the exact
approved snapshot. Nothing was sent or exported. Approval never transfers to
another revision, even when its bytes are identical. A newer-version warning
means this operation still targets the older snapshot; inspect it explicitly or
request approval for the intended newer accepted proposal.

## 7. Check mismatched confirmation and zero-call replay

The browser exposes replay verification for agent runs under Evidence. The local
engine API also reports counted model and tool-executor calls for both model runs
and the successful action. This small standard-library client keeps its local
CSRF cookie in memory; it is not an authentication client.

Save the following as a **private local scratch file**, not inside the example's
sources. Set `BASE` to the exact printed loopback URL. Run with `CHECK=mismatch`
while exactly one gate is pending, then `CHECK=replay` after successful simulation:

```python
import http.cookiejar, json, os, urllib.error, urllib.request
base = os.environ["BASE"]
opener = urllib.request.build_opener(
    urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

def api(path, body=None):
    request = urllib.request.Request(base + path,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Origin": base})
    try:
        with opener.open(request, timeout=60) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)

code, state = api("/engine/api/state")  # also obtains the local CSRF cookie
assert code == 200
views = []
for operation in state.get("announcements", []):
    code, view = api("/engine/api/announcement?id=" + operation["id"])
    assert code == 200
    views.append(view)
if os.environ["CHECK"] == "mismatch":
    pending = [v for v in views if v["status"] == "pending-approval"]
    assert len(pending) == 1, "select an unambiguous pending operation"
    view = pending[0]
    bad = dict(view["confirmation"], confirmed=True, digest="0" * 64)
    code, _ = api("/engine/api/announcement/decision", {
        "id": view["id"], "role": "GM", "approved": True, "confirmation": bad})
    assert code == 409
    code, after = api("/engine/api/announcement?id=" + view["id"])
    assert code == 200 and after["status"] == "pending-approval"
    print("Mismatched confirmation refused; still pending.")
else:
    assert os.environ["CHECK"] == "replay"
    bodies = [("/engine/api/verify", {"run": r["id"]}) for r in state["runs"]]
    simulated = [v for v in views if v["status"] == "simulated"]
    assert len(simulated) == 1, "expected one successful simulation"
    bodies.append(("/engine/api/announcement/verify", {"id": simulated[0]["id"]}))
    for path, body in bodies:
        code, proof = api(path, body)
        assert code == 200, proof
        assert all(proof[k] for k in
            ("chainValid", "reconstructed", "outputsMatch", "liveUnchanged"))
        assert proof["modelCalls"] == 0 and proof["executorCalls"] == 0
        print(body, "replay: 0 model / 0 tool calls")
```

For example, using your actual printed URL and private script path:

```sh
BASE=http://127.0.0.1:YOUR_PORT CHECK=mismatch python3 /path/to/check.py
# After explicit approval and Continue:
BASE=http://127.0.0.1:YOUR_PORT CHECK=replay python3 /path/to/check.py
```

Replay reconstructs recorded execution with counted fail-on-call model/tool
adapters. It is integrity evidence, not another evaluation or renewed consent.
You are done when the critique addressed accepted content, the revised proposal
was reviewed, denial produced no simulation, restart preserved a pending gate,
approval alone did nothing, Continue produced one receipt, and replay reported
zero calls. Stop Canvas and preserve state for later inspection. For another
workspace, select a new dedicated state path; there is no reset or conversion
command. A fresh operator's walkthrough is a separate usability pass.

Next: [package and suite reproduction](../examples/release-review/README.md).
AKS, durable-host restart, authenticated identity/roles and shared-use approval
qualification are separate work, not established by this local tutorial.
