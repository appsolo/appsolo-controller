PROJECT ?= appsolo-controller
REGISTRY ?= ghcr.io/appsolo
PLATFORMS ?= linux/amd64,linux/arm64
VERSION ?= $(shell git describe --tags --match "v*.*" HEAD)
TAG ?= $(VERSION)
NOCACHE ?= false

# go source files, ignore vendor directory
SRC = $(shell find . -type f -name '*.go' -not -path "./vendor/*")

help:
	@echo "Useful targets: 'update', 'upload' 'check' 'test"

all: check test update upload

.PHONY: update
update:
	for r in $(REGISTRY); do \
		docker buildx build $(_EXTRA_ARGS) \
			--build-arg=VERSION=$(VERSION) \
			--platform=$(PLATFORMS) \
			--no-cache=$(NOCACHE) \
			--pull=$(NOCACHE) \
			--tag $$r/$(PROJECT):$(TAG) \
			--tag $$r/$(PROJECT):latest \
			. ;\
	done

.PHONY: upload
upload:
	make update _EXTRA_ARGS=--push

check:
	go mod tidy
	test -z "$(git status --porcelain)"
	test -z $(shell gofmt -l main.go | tee /dev/stderr) || echo "[WARN] Fix formatting issues with 'make fmt'"
	golangci-lint run
	go vet ./...

test:
	go test -timeout 30s github.com/appsolo/appsolo-controller/pkg/.../ -v

fmt:
	@gofmt -l -w $(SRC)

simplify:
	@gofmt -s -l -w $(SRC)