# Your first review canvas

Make a canvas of your own, run it locally, and accept or reject a document
proposal. Allow about 15 minutes after the prerequisites are installed. This
walkthrough uses a deterministic fixture, not a model service. It needs no
credentials, Copilot app, container, cluster or kmx Lift.

The goal is **one reviewed proposal**, not merely a valid YAML file.

## 1. Choose a new package and state directory

Run the commands from a Canvas source checkout with Go 1.27 available. The first
`go run` may download the pinned Go toolchain and modules; complete that setup
before working offline. Canvas's offline validator itself does not fetch a
provider or registry.

Choose two unused paths under a real directory, not a symlinked directory alias:

```sh
PACKAGE="$HOME/first-review"
STATE="$HOME/first-review-state"

go run ./cmd/canvas init "$PACKAGE" --fixture \
  --template single-review --name launch-notes
```

Init refuses an existing destination, including an empty directory. If one of
these paths is already in use, choose another name rather than deleting it.
The single-review template has one writer, one JSON document, an explicit
read/propose scope and a manual trigger. Its complete pinned fixture layout is
copied from the checkout into the new package; no registry fetch occurs.

For two agents and two documents, use `--template writer-review` instead. That
adds a reviewer and a separate review document, not authenticated human reviewers
or independent model-quality verification.

## 2. Make the starting document yours

Open `seeds/draft.json` in the generated package and replace its contents with:

```json
{"text":"A fictional release note needs a short explanation of the review process."}
```

You can also edit the definition's metadata name/title and display labels. For
this fixture walkthrough, leave document/agent IDs, paths, schema bytes, suite
pins, scopes and triggers as generated. Fixture eligibility is checked from
those controls, not from a claim that a file came from init. There are no actions
or automatic triggers.

The schema requires a JSON object containing a nonempty `text` string, at most
4096 characters. Other fields are not accepted.

## 3. Validate without running

```sh
go run ./cmd/canvas validate "$PACKAGE/canvas.yaml" --json
```

Expect `ok: true` and `status: "valid-offline"`. An invalid schema, seed, reference
or local suite refuses. No runtime state, provider, Sessions host or listener
starts. Fix an invalid seed before continuing; don't change a pin or blob just
to make validation pass.

## 4. Deliberately start your canvas

In the same terminal:

```sh
go run ./cmd/canvas --fixture --definition "$PACKAGE/canvas.yaml" \
  --state "$STATE" --listen 127.0.0.1:0
```

Open the printed **Canvas:** URL in your browser. Port 0 chooses an available
loopback port. The engine prints a fixture-provider notice, and the page displays
**Deterministic fixture provider. No real inference.**

An embedded Sessions host records local runs. It uses the existing Canvas engine,
not another runtime. The suite's authored model name remains visible as identity;
it is not evidence that that model was called. Nothing runs simply because the
page opens. Demo personas are attribution, not sign-in or approval authority.

Fixture mode never loads ambient model configuration and refuses explicit
provider, remote-host, host-only and validation flags. It cannot fall back to a
real model.

## 5. Request and review a proposal

1. Open **New… → New agent run**.
2. Select the writer, enter a request such as “Show a local proposal for review,”
   and choose **Request run**.
3. Inspect the proposed change in **Document review**. The accepted content is
   your edited seed; the proposed replacement is visibly different. Open the
   full accepted/proposed text and evidence as needed.
4. Choose **Accept proposal** to save the proposed document, or **Reject** to
   leave accepted content unchanged. The decision covers the whole proposal.

The fixture returns deliberately fixed text; it does not interpret your request
or assess your writing. It reads the run's current accepted snapshot and always
proposes a different schema-valid value, including after a seed edit or a
previous acceptance. This tests the review path, not model quality.

You have finished the tutorial when you have inspected the diff and recorded
one acceptance or rejection. For an extra check, request another run and reject
it: the last accepted document must remain unchanged.

## 6. Stop and reopen without mixing modes

Press Ctrl+C in the terminal. Run the same command with the same definition and
state to reopen. Use the newly printed URL; opening the page does not resume or
create work. Recorded runs and accepted documents remain available.

The state root stores `runtime-mode.json` before starting a host:

- A new fixture requires a missing or empty directory.
- Existing unmarked state remains usable by ordinary real/configured startup;
  that startup records its mode before host activation without rewriting history.
- Recorded fixture state cannot start in real mode, or vice versa.
- Corrupt or linked mode metadata refuses without repair or history changes.

If you change the definition or seed after starting, use a separate new state
path rather than changing an existing state binding. No reset or conversion
command is provided. Keep this state dedicated to this command:
older binaries and other commands do not enforce the new mode marker. It is a
provider-mode guard, not OS containment or retroactive proof of historical model
execution.

Next: [authoring CLI and machine-readable results](authoring-cli.md). Real-agent
integration, Copilot-assisted drafting, setup-file acceptance and Lift remain
separate from this local fixture walkthrough.
