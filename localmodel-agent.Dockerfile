# Build the manager binary
FROM registry.access.redhat.com/ubi9/go-toolset:1.25 as builder

# Copy in the go src
WORKDIR /go/src/github.com/kserve/kserve
COPY go.mod  go.mod
COPY go.sum  go.sum

RUN go mod download

COPY LICENSE LICENSE
COPY hack/tools.go hack/tools.go

ARG CMD=localmodelnode
COPY cmd/${CMD}/ cmd/${CMD}/
COPY pkg/    pkg/

# Build
ARG GOTAGS=""
RUN CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=mod go build -tags "${GOTAGS}" -a -o localmodelnode-agent ./cmd/${CMD}

# Generate third-party licenses (tool is declared in hack/tools.go and pinned in go.mod)
# Forbidden Licenses: https://github.com/google/licenseclassifier/blob/e6a9bb99b5a6f71d5a34336b8245e305f5430f99/license_type.go#L341
RUN go run github.com/google/go-licenses/v2 check ./cmd/${CMD} ./pkg/... --disallowed_types="forbidden,unknown"
RUN go run github.com/google/go-licenses/v2 save --save_path third_party/library ./cmd/${CMD}

# Copy the controller-manager into a thin image
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /go/src/github.com/kserve/kserve/third_party /third_party
COPY --from=builder /go/src/github.com/kserve/kserve/localmodelnode-agent /manager
ENTRYPOINT ["/manager"]
