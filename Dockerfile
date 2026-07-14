# Build the manager binary
FROM registry.access.redhat.com/ubi9/go-toolset:1.25 AS deps
# distro: UBI go-toolset does not add GOPATH/bin to PATH
ENV PATH="$PATH:/opt/app-root/src/go/bin"

WORKDIR /go/src/github.com/kserve/kserve
COPY go.mod  go.mod
COPY go.sum  go.sum
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# ---- Build stage (parallel with license on BuildKit) ----
FROM deps AS builder

ARG CMD=manager
ARG GOTAGS=""
COPY cmd/${CMD}/ cmd/${CMD}/
COPY pkg/    pkg/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=readonly go build -a -tags "${GOTAGS}" -o manager ./cmd/${CMD}

# ---- License stage (parallel with build on BuildKit) ----
FROM deps AS license

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go install github.com/google/go-licenses@v1.6.0

ARG CMD=manager
COPY cmd/${CMD}/ cmd/${CMD}/
COPY pkg/    pkg/
COPY LICENSE LICENSE
RUN --mount=type=cache,target=/go/pkg/mod \
    go-licenses check ./cmd/${CMD} ./pkg/... --disallowed_types="forbidden,unknown" && \
    go-licenses save --save_path third_party/library ./cmd/${CMD}

# Runtime image - Copy the controller-manager into a thin image
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

RUN microdnf install -y --disablerepo=* --enablerepo=ubi-9-baseos-rpms shadow-utils && \
    microdnf clean all && \
    useradd kserve -m -u 1000
RUN microdnf remove -y shadow-utils

COPY --from=license /go/src/github.com/kserve/kserve/third_party /third_party
COPY --from=builder /go/src/github.com/kserve/kserve/manager /manager
USER 1000:1000

ENTRYPOINT ["/manager"]
