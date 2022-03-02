# Generate the default image tag based on the git branch and revision.
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
GIT_REVISION := $(shell git rev-parse --short HEAD)
IMAGE_TAG ?= $(GIT_BRANCH)-$(GIT_REVISION)

rollout-operator:
	go build ./cmd/rollout-operator

.PHONY: build-linux-amd64
build-linux-amd64:
	GOOS=linux GOARCH=amd64 go build ./cmd/rollout-operator

.PHONY: build-image
build-image: build-linux-amd64
	docker build --build-arg "$(GIT_REVISION)" -t rollout-operator:latest -t rollout-operator:$(IMAGE_TAG) .

.PHONY: publish-image
publish-image: build-image
	docker tag rollout-operator:$(IMAGE_TAG) grafana/rollout-operator:$(IMAGE_TAG)
	docker push grafana/rollout-operator:$(IMAGE_TAG)

.PHONY: test
test:
	go test ./...

.PHONY: lint
lint:
	golangci-lint run --timeout=5m
