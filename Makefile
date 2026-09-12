.PHONY: run build test test-go test-unit test-integration test-ui test-e2e test-backends test-docker test-all assets check-assets
GO ?= go
PYTHON ?= python3
assets:
	python3 tools/vendor.py
check-assets:
	python3 tools/vendor.py --check
run: assets
	go run ./cmd/mori
build: assets
	mkdir -p bin
	go build -trimpath -o bin/mori ./cmd/mori
test: test-go test-unit test-integration
test-go:
	$(GO) test -race -cover ./...
	$(GO) vet ./...
test-unit:
	$(PYTHON) -m unittest tests.unit.vendor_test
	node --check web/app.js
	node --check web/preview.js
test-integration:
	$(PYTHON) -m tests.integration.compose_smoke
	$(PYTHON) -m tests.integration.http_smoke
test-ui:
	$(PYTHON) -m tests.ui.ui_smoke
	$(PYTHON) -m tests.ui.preview_dom
test-e2e:
	$(PYTHON) -m tests.e2e.http_e2e
	$(PYTHON) -m tests.e2e.preview_e2e
	MORI_TEST_BROWSER=webkit $(PYTHON) -m tests.e2e.preview_e2e
test-backends:
	sh tests/e2e/backends_e2e.sh
test-docker:
	docker build -t mori:unified-test .
	$(PYTHON) -m tests.e2e.docker_smoke
test-all: test test-ui test-e2e test-backends test-docker
