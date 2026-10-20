HTTP_PORT ?= 8765
MODEL_PORT ?= 11434
MODEL_PROFILE ?=
CONTAINER_ENGINE ?= auto
# Python 3.10+. Point at a venv to use its packages, e.g. PYTHON=.venv/bin/python.
PYTHON ?= python3

.PHONY: setup setup-offline dev run northwind test fmt-check browser-smoke build check-model check-offline-model remove-ollama
setup:
	go mod download
setup-offline: setup
	$(PYTHON) scripts/ollama.py start --port $(MODEL_PORT) --container-engine "$(CONTAINER_ENGINE)"
dev:
	$(PYTHON) scripts/dev.py --http-port $(HTTP_PORT) --model-port $(MODEL_PORT) --container-engine "$(CONTAINER_ENGINE)" $(if $(MODEL_PROFILE),--model-profile "$(MODEL_PROFILE)")
run:
	go run ./cmd/canvas -listen 127.0.0.1:$(HTTP_PORT) -model-base http://127.0.0.1:$(MODEL_PORT) $(if $(MODEL_PROFILE),-model-profile "$(MODEL_PROFILE)")
# Direct Northwind start; uses separate state and an already configured model endpoint.
northwind:
	go run ./cmd/canvas -definition examples/northwind-prd/canvas.yaml -state .state/northwind -listen 127.0.0.1:$(HTTP_PORT) -model-base http://127.0.0.1:$(MODEL_PORT) $(if $(MODEL_PROFILE),-model-profile "$(MODEL_PROFILE)")
# CI runs these same targets.
test: fmt-check
	go test -race ./...
	go vet ./...
	node --check web/app.js
	@for file in .github/extensions/northwind-review/*.js .github/extensions/northwind-review/*.mjs; do node --check "$$file" || exit 1; done
	node --test scripts/*.test.cjs
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -m unittest discover -s scripts -p '*_test.py'
# Skips hidden directories so local worktrees and caches aren't checked.
fmt-check:
	@files=$$(find . -path '*/.*' -prune -o -name '*.go' -print); \
	unformatted=$$(gofmt -l $$files); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed for:"; echo "$$unformatted"; gofmt -d $$unformatted; exit 1; fi
# Optional; needs Playwright with Chromium (see the script header). Uses its own loopback port.
browser-smoke:
	node scripts/browser-smoke.cjs
build:
	go build ./cmd/...
check-model:
	go run ./cmd/sessions-host -check-model -model-base http://127.0.0.1:$(MODEL_PORT) $(if $(MODEL_PROFILE),-model-profile "$(MODEL_PROFILE)")
check-offline-model:
	$(PYTHON) scripts/ollama.py verify --port $(MODEL_PORT)
remove-ollama:
	$(PYTHON) scripts/ollama.py remove --port $(MODEL_PORT) --container-engine "$(CONTAINER_ENGINE)"
