# Development and verification

[Docs index](README.md) · [Canvas overview](../README.md)

```sh
make test    # gofmt check, Go tests with -race, vet, UI tests, Python tests
make build
```

`make test` checks the shipped offline default in isolation; your ignored local
configuration does not change the config-dependent CLI tests.

The Python helpers and tests use only the Python 3.10+ standard library.
CI selects Python 3.12 and runs the same `make test`. You can select another
interpreter with `make test PYTHON=/path/to/python`.
The approval DOM tests run in headless Chromium. They use Playwright 1.63.0
from `PLAYWRIGHT_MODULE` (an absolute package path), or the global npm install.
CI installs this pinned development tool with npm scripts disabled, audits its
temporary lockfile, and explicitly installs Chromium. It is not a runtime
application dependency.

`make browser-smoke` runs an end-to-end browser check against a test backend
(needs Playwright with Chromium; see the top of `scripts/browser-smoke.cjs`).
Its owned fixture uses an independent OS-assigned loopback port, so it does not
collide with a running demo. It cleans up that fixture on completion,
startup/browser failures and interrupts; it never stops an externally supplied
fixture. Process-lifecycle regressions run in `make test` without requiring
Playwright.

## Local development startup

`make dev` starts the generic Northwind example with embedded local Sessions,
explicitly selecting `examples/northwind-prd/canvas.yaml` and checkout-local
`.state/northwind`. It does not migrate or reset existing legacy Draft Room
state. The launcher reads the local model configuration first, prepares the
owned Ollama resources only for offline mode, builds the commands, then runs the
finite `sessions-host -check-model` metadata diagnostic before starting Canvas.
Startup does not request model generation; requesting an agent or changing a
triggered input later can generate output and incur hosted charges.

Canvas is the only runtime child; Ctrl-C stops it and its embedded host. The
existing Ollama container/model volume persist. Override `HTTP_PORT` (default
8765), offline `MODEL_PORT` (11434), `MODEL_PROFILE`, or `CONTAINER_ENGINE` as
needed; see [model setup](local-models.md). Hosted mode ignores `MODEL_PORT`.
Sessions now uses an OS-assigned internal loopback port: `SESSIONS_PORT` is no
longer used by Make and `scripts/dev.py --sessions-port` is no longer accepted.
Only the HTTP port is preflighted; an offline model port must differ from it.

`make northwind`, `make run` and the flagless Canvas command start Northwind
against an already configured model endpoint, without the launcher's setup and
finite diagnostic. The Canvas default is `examples/northwind-prd/canvas.yaml`
with `.state/northwind` state. `--definition` selects another generic definition;
keep its state separate. Empty definitions and `--sessions-address` are refused.

The standalone `sessions-host` also defaults to Northwind, with its own
`.state/northwind-host` directory and `sessions` subdirectory. It is host-only
support, not a prerequisite or remote runtime for generic Canvas. Substrate
startup is no longer available from these generic command defaults. Model
metadata checks still return before any definition or state open.

Explicitly selecting old football state is refused before cache/runtime writes,
permission changes or model-provider setup. Existing files are not migrated or
reset. Successful default startup may create the new `northwind` child under
`.state`, without changing existing legacy entries, bytes or modes.

## Verification limits

The football backend, example, live-smoke script and reset command are removed.
Use `make browser-smoke` for the generic review workflow against its own
model-free fixture; it never targets a running workspace or qualifies real-model
execution. The test suite retains historical session profiles, archive readback
and strict recorded replay without regenerating the historical fixtures.

None of the tests call a real model, so passing tests don't tell you anything
about model quality. A model-backed walkthrough requires a separately configured
provider, explicit generation and review, and its own recorded evidence; see the
[release-notes tutorial](review-release-notes-with-two-agents.md).
