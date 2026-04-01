# Build the inference-agent binary
FROM registry.access.redhat.com/ubi9/go-toolset:1.25 AS builder
# distro: UBI go-toolset does not add GOPATH/bin to PATH
ENV PATH="$PATH:/opt/app-root/src/go/bin"

WORKDIR /go/src/github.com/kserve/kserve
COPY go.mod  go.mod
COPY go.sum  go.sum
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

ARG CMD=agent
COPY cmd/${CMD}/ cmd/${CMD}/
COPY pkg/    pkg/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=readonly go build -a -o agent ./cmd/${CMD}

# Check and generate third-party licenses (fast, fail-fast on violations)
RUN /opt/app-root/src/go/bin/go-licenses check ./cmd/${CMD} ./pkg/... --disallowed_types="forbidden,unknown" && \
    /opt/app-root/src/go/bin/go-licenses save --save_path third_party/library ./cmd/${CMD}

# Copy the inference-agent into a thin image
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

RUN microdnf install -y --disablerepo=* --enablerepo=ubi-9-baseos-rpms shadow-utils && \
    microdnf clean all && \
    useradd kserve -m -u 1000
RUN microdnf remove -y shadow-utils

COPY --from=builder /go/src/github.com/kserve/kserve/third_party /third_party

WORKDIR /ko-app

COPY --from=builder /go/src/github.com/kserve/kserve/agent /ko-app/
USER 1000:1000

ENTRYPOINT ["/ko-app/agent"]
