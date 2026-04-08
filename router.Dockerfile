# Build the inference-router binary
FROM registry.access.redhat.com/ubi9/go-toolset:1.25 as builder

# Copy in the go src
WORKDIR /go/src/github.com/kserve/kserve
COPY go.mod  go.mod
COPY go.sum  go.sum

RUN go mod download

COPY LICENSE LICENSE
COPY hack/tools.go hack/tools.go

ARG CMD=router
COPY cmd/${CMD}/ cmd/${CMD}/
COPY pkg/    pkg/

# Build
USER root
RUN CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=mod go build -a -o router ./cmd/${CMD}

# Generate third-party licenses (tool is declared in hack/tools.go and pinned in go.mod)
# Forbidden Licenses: https://github.com/google/licenseclassifier/blob/e6a9bb99b5a6f71d5a34336b8245e305f5430f99/license_type.go#L341
RUN go run github.com/google/go-licenses/v2 check ./cmd/${CMD} ./pkg/... --disallowed_types="forbidden,unknown"
RUN go run github.com/google/go-licenses/v2 save --save_path third_party/library ./cmd/${CMD}

# Copy the inference-router into a thin image
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
RUN microdnf install -y --disablerepo=* --enablerepo=ubi-9-baseos-rpms shadow-utils && \
    microdnf clean all && \
    useradd kserve -m -u 1000
RUN microdnf remove -y shadow-utils

COPY --from=builder /go/src/github.com/kserve/kserve/third_party /third_party

WORKDIR /ko-app

COPY --from=builder /go/src/github.com/kserve/kserve/router /ko-app/
USER 1000:1000

ENTRYPOINT ["/ko-app/router"]
