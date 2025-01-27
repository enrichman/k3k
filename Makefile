
REPO ?= rancher
BUILDER := $(REPO)/k3k-builder:latest
VERSION := $(shell ./scripts/version)

# Use GOOS and GOARCH from the environment variables, or fallback to go env
HOST_GOOS ?= $(shell go env GOOS)
HOST_GOARCH ?= $(shell go env GOARCH)
GOCACHE ?= $(shell go env GOCACHE)

DOCKER_RUN := docker run -it --rm \
	-e VERSION=$(VERSION) \
	-e HOST_GOOS=$(HOST_GOOS) \
	-e HOST_GOARCH=$(HOST_GOARCH) \
	-v ${GOCACHE}:/root/.cache/go-build \
	-v $(CURDIR):/go/src \
	$(BUILDER)

.PHONY: all
all: docker-builder build docker-package

# Output the current version
.PHONY: version
version:
	@./scripts/version

# Build the custom Docker builder image
.PHONY: docker-builder
docker-builder:
	docker build -t $(BUILDER) .

# Build the project using the custom Docker builder image
.PHONY: build
build:
	./scripts/build

# Build the project using the custom Docker builder image
.PHONY: build-docker
build-docker: docker-builder
	$(DOCKER_RUN) ./scripts/build

# Package Docker images
.PHONY: docker-package
docker-package: docker-package-k3k docker-package-k3k-kubelet

.PHONY: docker-package-%
docker-package-%: build
	docker build -f package/Dockerfile.$* -t $(REPO)/$*:$(VERSION) -t $(REPO)/$*:latest  -t $(REPO)/$*:dev .

# Push images to the registry
.PHONY: docker-push
docker-push: docker-push-k3k docker-push-k3k-kubelet

.PHONY: docker-push-%
docker-push-%: docker-package-%
	docker push $(REPO)/$*:$(VERSION)
	docker push $(REPO)/$*:latest

.PHONY: build-crds
build-crds:
	@# This will return non-zero until all of our objects in ./pkg/apis can generate valid crds.
	@# allowDangerousTypes is needed for struct that use floats
	controller-gen crd:generateEmbeddedObjectMeta=true,allowDangerousTypes=false \
		paths=./pkg/apis/... \
		output:crd:dir=./charts/k3k/crds

.PHONY: test
test:
	ginkgo -v -r --skip-file=tests/*

.PHONY: lint
lint: docker-builder
	golangci-lint run --timeout=5m
