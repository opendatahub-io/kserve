# Build the manager binary
FROM registry.access.redhat.com/ubi9/go-toolset:1.24 AS builder

# Copy in the go src
WORKDIR /go/src/github.com/kserve/kserve
COPY go.mod  go.mod
COPY go.sum  go.sum

# Allow Go to automatically download the required toolchain version
ENV GOTOOLCHAIN=auto
RUN go mod download

COPY cmd/    cmd/
COPY pkg/    pkg/

# Build
USER root
RUN CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=mod go build -a -o manager ./cmd/manager

# Generate third-party licenses
COPY LICENSE LICENSE
# go-licenses is temporarily disabled due to incompatibility with Go 1.24+
# go-licenses v1.6.0 has a compilation error with Go 1.24 due to github.com/otiai10/copy dependency
# go-licenses v2 is available as pre-release but not yet stable
# See: https://github.com/google/go-licenses/issues/312
# Uncomment these lines once go-licenses v2 is stable or v1.x adds Go 1.24+ support
# RUN go install github.com/google/go-licenses@latest
# Forbidden Licenses: https://github.com/google/licenseclassifier/blob/e6a9bb99b5a6f71d5a34336b8245e305f5430f99/license_type.go#L341
# RUN go-licenses check ./cmd/... ./pkg/... --disallowed_types="forbidden,unknown"
# RUN go-licenses save --save_path third_party/library ./cmd/manager

# Use distroless as minimal base image to package the manager binary
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
RUN microdnf install -y --disablerepo=* --enablerepo=ubi-9-baseos-rpms shadow-utils && \
    microdnf clean all && \
    useradd kserve -m -u 1000
RUN microdnf remove -y shadow-utils
# COPY third_party/ /third_party/
COPY --from=builder /go/src/github.com/kserve/kserve/manager /
USER 1000:1000

ENTRYPOINT ["/manager"]
