.PHONY: run build test assets check-assets
assets:
	python3 tools/vendor.py
check-assets:
	python3 tools/vendor.py --check
run: assets
	go run ./cmd/mori
build: assets
	mkdir -p bin
	go build -trimpath -o bin/mori ./cmd/mori
test:
	go test -race -cover ./...
	go vet ./...
