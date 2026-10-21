# go-heic-lossless-rotate
#
# Binaries build to ./bin/. The differential suite (difftest) runs the
# pinned upstream Python implementation; `make upstream` fetches it.

UPSTREAM_REPO   ?= https://github.com/olegoz/heic-lossless-rotate.git
UPSTREAM_COMMIT := $(shell cat difftest/upstream.commit)
UPSTREAM_DIR    ?= .upstream

.PHONY: all build run test difftest upstream fmt lint release-check snapshot clean

all: fmt lint test build

## build: compile all packages; the heic-rotate and heic-lossless-rotate binaries land in ./bin/
build:
	go build ./...
	@if [ -d cmd ]; then mkdir -p bin && go build -o bin/ ./cmd/...; fi

## run: build, then run heic-rotate with ARGS (e.g. make run ARGS="info photo.heic")
run: build
	./bin/heic-rotate $(ARGS)

## test: unit tests (no Python required)
test:
	go test -race ./...

## difftest: differential tests against the pinned upstream Python implementation
difftest: upstream
	HEIC_ROTATE_PY_SRC=$(abspath $(UPSTREAM_DIR)) go test -tags difftest -count=1 ./difftest/...

## upstream: clone (if missing) and check out the pinned upstream commit
upstream:
	@if [ ! -d "$(UPSTREAM_DIR)/.git" ]; then \
		git clone --quiet $(UPSTREAM_REPO) $(UPSTREAM_DIR); \
	fi
	@if [ "$$(git -C $(UPSTREAM_DIR) rev-parse HEAD)" != "$(UPSTREAM_COMMIT)" ]; then \
		git -C $(UPSTREAM_DIR) fetch --quiet origin && \
		git -C $(UPSTREAM_DIR) checkout --quiet --detach $(UPSTREAM_COMMIT); \
	fi
	@echo "upstream: $(UPSTREAM_DIR) at $(UPSTREAM_COMMIT)"

## fmt: format all Go code
fmt:
	go fmt ./...

## lint: go vet (both build modes), plus staticcheck when installed
lint:
	go vet ./...
	go vet -tags difftest ./...
	@if command -v staticcheck >/dev/null 2>&1; then \
		echo "staticcheck ./..." && staticcheck ./... && \
		echo "staticcheck -tags difftest ./..." && staticcheck -tags difftest ./...; \
	else \
		echo "staticcheck not installed; skipping (go install honnef.co/go/tools/cmd/staticcheck@latest)"; \
	fi

## release-check: check that TAG (e.g. make release-check TAG=v1.7.1) is the release tag for pkg/version
release-check:
	@test -n "$(TAG)" || { echo "usage: make release-check TAG=vX.Y.Z[+go.N]"; exit 2; }
	RELEASE_TAG=$(TAG) go test -count=1 -run '^TestReleaseTag$$' ./pkg/version

## snapshot: local goreleaser dry run (all platforms, nothing published); archives land in ./dist/
snapshot:
	goreleaser release --snapshot --clean

## clean: remove build outputs
clean:
	rm -rf bin dist
