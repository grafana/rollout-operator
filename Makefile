# Generate the default image tag based on the git branch and revision.
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
GIT_REVISION := $(shell git rev-parse --short HEAD)
IMAGE_PREFIX ?= grafana
IMAGE_TAG ?= $(GIT_BRANCH)-$(GIT_REVISION)

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

DONT_FIND := -name vendor -prune -o -name .git -prune -o -name .cache -prune -o -name .pkg -prune -o
GO_FILES := $(shell find . $(DONT_FIND) -name cmd -prune -o -name '*.pb.go' -prune -o -type f -name '*.go' -print)

rollout-operator: $(GO_FILES)
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' ./cmd/rollout-operator

.PHONY: build-image
build-image: clean
	docker buildx build --load --platform linux/amd64 --build-arg revision=$(GIT_REVISION) -t rollout-operator:latest -t rollout-operator:$(IMAGE_TAG) .

.PHONY: publish-image
publish-image: clean
	docker buildx build --push --platform linux/amd64,linux/arm64 --build-arg revision=$(GIT_REVISION) -t $(IMAGE_PREFIX)/rollout-operator:$(IMAGE_TAG) .

.PHONY: test
test:
	go test ./...

.PHONY: integration
integration: integration/mock-service/.uptodate
	go test -v -tags requires_docker -count 1 -timeout 1h ./integration/...

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
