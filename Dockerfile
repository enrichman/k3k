FROM rancher/hardened-build-base:v1.23.4b1

RUN apk -U add \
    bash git gcc musl-dev docker vim less file curl wget ca-certificates

ENV CONTROLLER_GEN_VERSION=v0.14.0
RUN go install sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_GEN_VERSION}

ENV GOLANGCI_LINT_VERSION=v1.60.3
RUN curl -sL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s ${GOLANGCI_LINT_VERSION}

ENV GINKGO_VERSION=v2.21.0
RUN go install github.com/onsi/ginkgo/v2/ginkgo@${GINKGO_VERSION}

ENV SETUP_ENVTEST_VERSION=latest
RUN go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest && \
    ENVTEST_BIN=$(setup-envtest use -p path) && \
    mkdir -p /usr/local/kubebuilder/bin && \
    cp $ENVTEST_BIN/* /usr/local/kubebuilder/bin    

ADD go.mod go.mod
RUN go mod download && rm go.mod

WORKDIR /go/src
