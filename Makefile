# Generate the default image tag based on the git branch and revision.
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
GIT_REVISION := $(shell git rev-parse --short HEAD)

IMAGE_PREFIX ?= grafana
IMAGE_TAG ?= $(subst /,-,$(GIT_BRANCH))-$(GIT_REVISION)

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

DONT_FIND := -name vendor -prune -o -name .git -prune -o -name .cache -prune -o -name .pkg -prune
GO_FILES := $(shell find . $(DONT_FIND) -o -type f -name '*.go' -print)

.DEFAULT_GOAL := rollout-operator

# Adapted from https://www.thapaliya.com/en/writings/well-documented-makefiles/
.PHONY: help
help: ## Display this help and any documented user-facing targets
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_\.\-\/%]+:.*?##/ { printf "  %-45s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

rollout-operator: $(GO_FILES) ## Build the rollout-operator binary
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' ./cmd/rollout-operator

rollout-operator-debug: $(GO_FILES)
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' ./cmd/rollout-operator

.PHONY: rollout-operator-boringcrypto
rollout-operator-boringcrypto: $(GO_FILES) ## Build the rollout-operator binary with boringcrypto
	GOEXPERIMENT=boringcrypto GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=1 go build -tags netgo ./cmd/rollout-operator

.PHONY: build-image 
build-image: clean ## Build the rollout-operator image
	docker buildx build --load --platform linux/amd64 --build-arg revision=$(GIT_REVISION) -t rollout-operator:latest -t rollout-operator:$(IMAGE_TAG) .

.PHONY: build-debug-image ## Build a rollout-operator image running in delve
build-debug-image: clean
	docker buildx build --load --platform linux/arm64 --build-arg revision=$(GIT_REVISION) -t rollout-operator:latest -t rollout-operator:$(IMAGE_TAG) -f Dockerfile.delve .

.PHONY: build-image-boringcrypto
build-image-boringcrypto: clean ## Build the rollout-operator image with boringcrypto 
	# Tags with the regular image repo for integration testing
	docker buildx build --load --platform linux/amd64 --build-arg revision=$(GIT_REVISION) --build-arg BUILDTARGET=rollout-operator-boringcrypto -t rollout-operator:latest -t rollout-operator:$(IMAGE_TAG) .

.PHONY: publish-images
publish-images: publish-standard-image publish-boringcrypto-image ## Build and publish both the standard and boringcrypto images

.PHONY: publish-standard-image
publish-standard-image: clean ## Build and publish only the standard rollout-operator image
	docker buildx build --push --platform linux/amd64,linux/arm64 --build-arg revision=$(GIT_REVISION) --build-arg BUILDTARGET=rollout-operator -t $(IMAGE_PREFIX)/rollout-operator:$(IMAGE_TAG) .

.PHONY: publish-debug-image
publish-debug-image: clean
	docker buildx build --push --platform linux/amd64,linux/arm64 --build-arg revision=$(GIT_REVISION) --build-arg BUILDTARGET=rollout-operator-debug -t $(IMAGE_PREFIX)/rollout-operator:$(IMAGE_TAG)-debug -f Dockerfile.delve .

.PHONY: publish-boringcrypto-image
publish-boringcrypto-image: clean ## Build and publish only the boring-crypto rollout-operator image
	docker buildx build --push --platform linux/amd64,linux/arm64 --build-arg revision=$(GIT_REVISION) --build-arg BUILDTARGET=rollout-operator-boringcrypto -t $(IMAGE_PREFIX)/rollout-operator-boringcrypto:$(IMAGE_TAG) .

.PHONY: test
test: ## Run tests
	go test ./...

.PHONY: test-boringcrypto
test-boringcrypto: ## Run tests with GOEXPERIMENT=boringcrypto
	GOEXPERIMENT=boringcrypto go test ./...

.PHONY: integration
integration: ## Run integration tests
integration: integration/mock-service/.uptodate
	go test -v -tags requires_docker -count 1 -timeout 1h ./integration/...

integration/mock-service/.uptodate:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' -o ./integration/mock-service/mock-service ./integration/mock-service
	docker buildx build --load --platform linux/amd64 --build-arg revision=$(GIT_REVISION) -t mock-service:latest -f ./integration/mock-service/Dockerfile ./integration/mock-service

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run --timeout=5m

.PHONY: clean
clean: ## Run go clean and remove the rollout-operator binary
	rm -f rollout-operator
	go clean ./...
