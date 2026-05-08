# Build the manager binary
FROM registry.access.redhat.com/ubi9/go-toolset:1.25 AS builder
ENV PATH="$PATH:/opt/app-root/src/go/bin"

WORKDIR /go/src/github.com/opendatahub-io/kserve-module
COPY kserve-module/go.mod  go.mod
COPY kserve-module/go.sum  go.sum
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY kserve-module/cmd/kserve-module/ cmd/kserve-module/
COPY kserve-module/pkg/              pkg/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=readonly go build -a -o manager ./cmd/kserve-module

# Collect kserve + odh-model-controller manifests
COPY kserve-module/hack/  hack/
COPY config/               ../config/
RUN bash hack/get_kserve_manifests.sh

# Runtime
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
RUN microdnf install -y --disablerepo=* --enablerepo=ubi-9-baseos-rpms shadow-utils && \
    microdnf clean all && \
    useradd kserve -m -u 1000 && \
    microdnf remove -y shadow-utils
COPY --from=builder /go/src/github.com/opendatahub-io/kserve-module/manager /manager
COPY --from=builder /go/src/github.com/opendatahub-io/kserve-module/opt/manifests/ /opt/manifests/
USER 1000:1000
ENTRYPOINT ["/manager"]
