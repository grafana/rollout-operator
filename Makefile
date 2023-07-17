# Generate the default image tag based on the git branch and revision.
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
GIT_REVISION := $(shell git rev-parse --short HEAD)
IMAGE_PREFIX ?= us.gcr.io/kubernetes-dev
IMAGE_TAG ?= $(subst /,-,$(GIT_BRANCH))-$(GIT_REVISION)

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

DONT_FIND := -name vendor -prune -o -name .git -prune -o -name .cache -prune -o -name .pkg -prune
GO_FILES := $(shell find . $(DONT_FIND) -o -type f -name '*.go' -print)

rollout-operator: $(GO_FILES)
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' ./cmd/rollout-operator

rollout-operator-boringcrypto: $(GO_FILES)
	GOEXPERIMENT=boringcrypto GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=1 go build -tags netgo ./cmd/rollout-operator

.PHONY: build-image
build-image: clean
	docker buildx build --load --platform linux/amd64 --build-arg revision=$(GIT_REVISION) -t rollout-operator:latest -t rollout-operator:$(IMAGE_TAG) .

build-image-boringcrypto: clean ## Build the rollout-operator image with boringcrypto and tag with the regular image repo, so that it can be used in integration tests.
	docker buildx build --load --platform linux/amd64 --build-arg revision=$(GIT_REVISION) --build-arg BUILDTARGET=rollout-operator-boringcrypto -t rollout-operator:latest -t rollout-operator:$(IMAGE_TAG) .

.PHONY: publish-images
publish-images: clean
	docker buildx build --push --platform linux/amd64,linux/arm64 --build-arg revision=$(GIT_REVISION) --build-arg BUILDTARGET=rollout-operator -t $(IMAGE_PREFIX)/rollout-operator:$(IMAGE_TAG) .
	docker buildx build --push --platform linux/amd64,linux/arm64 --build-arg revision=$(GIT_REVISION) --build-arg BUILDTARGET=rollout-operator-boringcrypto -t $(IMAGE_PREFIX)/rollout-operator-boringcrypto:$(IMAGE_TAG) .

.PHONY: test
test:
	go test ./...

test-boringcrypto:
	GOEXPERIMENT=boringcrypto go test ./...

.PHONY: integration
integration: integration/mock-service/.uptodate
	go test -v -tags requires_docker -count 1 -timeout 1h ./integration/...

.PHONY: integration-boringcrypto
integration-boringcrypto: integration/mock-service/.uptodate
	GOEXPERIMENT=boringcrypto go test -v -tags requires_docker -count 1 -timeout 1h ./integration/...

integration/mock-service/.uptodate:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' -o ./integration/mock-service/mock-service ./integration/mock-service
	docker buildx build --load --platform linux/amd64 --build-arg revision=$(GIT_REVISION) -t mock-service:latest -f ./integration/mock-service/Dockerfile ./integration/mock-service

.PHONY: lint
lint:
	golangci-lint run --timeout=5m

.PHONY: clean
clean:
	rm -f rollout-operator
	go clean ./...
