# Local settings (optional).
# WARNING: do not commit to a repository!
-include Makefile.local

# Generate the default image tag based on the git branch and revision.
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
GIT_REVISION := $(shell git rev-parse --short HEAD)

IMAGE_PREFIX ?= grafana
IMAGE_TAG ?= $(subst /,-,$(GIT_BRANCH))-$(GIT_REVISION)

# Support gsed on OSX (installed via brew), falling back to sed. On Linux
# systems gsed won't be installed, so will use sed as expected.
SED ?= $(shell which gsed 2>/dev/null || which sed)

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

DONT_FIND := -name vendor -prune -o -name .git -prune -o -name .cache -prune -o -name .pkg -prune
GO_FILES := $(shell find . $(DONT_FIND) -o -type f -name '*.go' -print)
MAKE_FILES := $(shell find . $(DONT_FIND) -o -name 'Makefile' -print -o -name '*.mk' -print)

MIXIN_PATH := operations/rollout-operator-mixin
MIXIN_OUT_PATH ?= operations/rollout-operator-mixin-compiled

.DEFAULT_GOAL := rollout-operator

REGO_POLICIES_PATH=operations/policies

# path to jsonnetfmt
JSONNET_FMT := jsonnetfmt

# path to the rollout-operator manifests
JSONNET_MANIFESTS_PATHS := operations

# Adapted from https://www.thapaliya.com/en/writings/well-documented-makefiles/
.PHONY: help
help: ## Display this help and any documented user-facing targets
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_\.\-\/%]+:.*?##/ { printf "  %-45s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

rollout-operator: $(GO_FILES) ## Build the rollout-operator binary
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' ./cmd/rollout-operator

.PHONY: rollout-operator-boringcrypto
rollout-operator-boringcrypto: $(GO_FILES) ## Build the rollout-operator binary with boringcrypto
	GOEXPERIMENT=boringcrypto GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=1 go build -tags netgo ./cmd/rollout-operator

.PHONY: build-image
build-image: clean ## Build the rollout-operator image
	docker buildx build --load --platform linux/amd64 --build-arg revision=$(GIT_REVISION) -t rollout-operator:latest -t rollout-operator:$(IMAGE_TAG) .

.PHONY: build-image-boringcrypto
build-image-boringcrypto: clean ## Build the rollout-operator image with boringcrypto
	# Tags with the regular image repo for integration testing
	docker buildx build --load --platform linux/amd64 --build-arg revision=$(GIT_REVISION) -t rollout-operator:latest -t rollout-operator:$(IMAGE_TAG) -f Dockerfile.boringcrypto .

.PHONY: publish-images
publish-images: publish-standard-image publish-boringcrypto-image ## Build and publish both the standard and boringcrypto images

.PHONY: publish-standard-image
publish-standard-image: clean ## Build and publish only the standard rollout-operator image
	docker buildx build --push --platform linux/amd64,linux/arm64 --build-arg revision=$(GIT_REVISION) -t $(IMAGE_PREFIX)/rollout-operator:$(IMAGE_TAG) .

.PHONY: publish-boringcrypto-image
publish-boringcrypto-image: clean ## Build and publish only the boring-crypto rollout-operator image
	docker buildx build --push --platform linux/amd64,linux/arm64 --build-arg revision=$(GIT_REVISION) -t $(IMAGE_PREFIX)/rollout-operator-boringcrypto:$(IMAGE_TAG) -f Dockerfile.boringcrypto .

.PHONY: release-notes 
release-notes: ## Generate the release notes for a GitHub release
	@echo "Docker images: \`${IMAGE_PREFIX}/rollout-operator:${IMAGE_TAG}\` and \`${IMAGE_PREFIX}/rollout-operator-boringcrypto:${IMAGE_TAG}\`\n\n## Changelog"
	@awk -v var="${IMAGE_TAG}" '$$0 ~ "## "var {flag=1; next} /^##/{flag=0} flag' CHANGELOG.md

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
lint: ## Run lints to check for style issues.
lint: check-makefiles
	golangci-lint run --timeout=5m

.PHONY: fix-lint
fix-lint: ## Automatically fix linting issues where possible
	golangci-lint run --timeout=5m --fix

.PHONY: clean
clean: ## Run go clean and remove the rollout-operator binary
	rm -f rollout-operator
	rm -rf integration/jsonnet-integration-tests
	go clean ./...

.PHONY: check-jsonnet-manifests
check-jsonnet-manifests: ## Check the rollout-operator manifests.
check-jsonnet-manifests: format-jsonnet
	@echo "Checking diff:"
	@./tools/find-diff-or-untracked.sh $(JSONNET_MANIFESTS_PATHS) || (echo "Please format jsonnet by running 'make format-jsonnet'" && false)

.PHONY: format-jsonnet
format-jsonnet: ## Format the rollout-operator manifests.
	@find $(JSONNET_MANIFESTS_PATHS) -type f -name '*.libsonnet' -print -o -name '*.jsonnet' -print | xargs $(JSONNET_FMT) -i

.PHONY: build-jsonnet-tests
build-jsonnet-tests: ## Build the rollout-operator tests.
	@./operations/rollout-operator-tests/build.sh

.PHONY: check-jsonnet-tests
check-jsonnet-tests: ## Check the jsonnet tests output.
check-jsonnet-tests: build-jsonnet-tests jsonnet-conftest-test
	@./tools/find-diff-or-untracked.sh operations/rollout-operator-tests || (echo "Please rebuild jsonnet tests output 'make build-jsonnet-tests'" && false)

.PHONY: jsonnet-conftest-quick-test
jsonnet-conftest-quick-test: ## Does not rebuild the yaml manifests, use the target jsonnet-conftest-test for that
jsonnet-conftest-quick-test:
	@./operations/rollout-operator-tests/run-conftest.sh --policies-path $(REGO_POLICIES_PATH) --manifests-path ./operations/rollout-operator-tests

.PHONY: jsonnet-conftest-test
jsonnet-conftest-test: build-jsonnet-tests jsonnet-conftest-quick-test

.PHONY: check-makefiles
check-makefiles: ## Check the makefiles format.
check-makefiles:
	@git diff --exit-code -- $(MAKE_FILES) || (echo "Please format Makefiles by running 'make format-makefiles'" && false)

.PHONY: format-makefiles
format-makefiles: ## Format all Makefiles.
format-makefiles: $(MAKE_FILES)
	$(SED) -i -e 's/^\(\t*\)  /\1\t/g' -e 's/^\(\t*\) /\1/' -- $?

.PHONY: check-mixin
check-mixin: ## Build, format and check the mixin files.
check-mixin: build-mixin format-jsonnet check-mixin-jb
	@echo "Checking diff:"
	./tools/find-diff-or-untracked.sh $(MIXIN_PATH) "$(MIXIN_OUT_PATH)" || (echo "Please build and format mixin by running 'make build-mixin format-jsonnet'" && false);

.PHONY: check-mixin-jb
check-mixin-jb:
	@cd $(MIXIN_PATH) && \
	jb install

.PHONY: build-mixin
build-mixin: ## Generates the rollout-operator mixin zip file.
build-mixin: check-mixin-jb
	@# Empty the compiled mixin directories content, without removing the directories itself,
	@# so that Grafana can refresh re-build dashboards when using "make mixin-serve".
	@echo "Generating compiled dashboard:"
	@mkdir -p "$(MIXIN_OUT_PATH)"
	@find "$(MIXIN_OUT_PATH)" -type f -delete;
	mixtool generate dashboards --directory "$(MIXIN_OUT_PATH)/dashboards" "${MIXIN_PATH}/mixin.libsonnet";
	@echo "sample rollout-operator dashboard generated to $(MIXIN_OUT_PATH)/dashboards"

mixin-serve: ## Runs Grafana loading the mixin dashboards.
	@./operations/rollout-operator-mixin-tools/serve/run.sh -p $(MIXIN_OUT_PATH)
