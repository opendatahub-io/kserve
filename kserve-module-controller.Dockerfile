# Build the manager binary
FROM registry.access.redhat.com/ubi9/go-toolset:1.25 AS builder
ENV PATH="$PATH:/opt/app-root/src/go/bin"

WORKDIR /go/src/github.com/kserve/kserve
COPY go.mod  go.mod
COPY go.sum  go.sum
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

ARG CMD=kserve-module
COPY cmd/${CMD}/ cmd/${CMD}/
COPY pkg/    pkg/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=readonly go build -a -o manager ./cmd/${CMD}

# Collect manifests (local dev builds only; Konflux uses prefetch task)
FROM builder AS manifests
ARG YQ_VERSION=v4.52.1
RUN curl -sL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64" -o /usr/local/bin/yq && \
    chmod +x /usr/local/bin/yq
COPY build/            build/
COPY config/           config/
COPY get_kserve_manifests.sh .
RUN bash get_kserve_manifests.sh

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
COPY --from=builder /go/src/github.com/kserve/kserve/manager /manager
COPY --from=manifests /go/src/github.com/kserve/kserve/opt/manifests/ /opt/manifests/
USER 1000:1000
ENTRYPOINT ["/manager"]
