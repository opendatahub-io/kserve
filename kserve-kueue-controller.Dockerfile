# Placeholder Dockerfile for the KServe Kueue controller, to be replaced once ./cmd/kueue exists.

FROM registry.access.redhat.com/ubi9/go-toolset:1.26 AS builder

USER 0

WORKDIR /workspace

RUN printf '%s\n' \
    'package main' \
    '' \
    'import "os"' \
    '' \
    'func main() {' \
    '	os.Stderr.WriteString("kserve-kueue-controller: placeholder image\n")' \
    '	os.Exit(1)' \
    '}' \
    > main.go && \
    go mod init placeholder && \
    CGO_ENABLED=0 GOOS=linux go build -a -o manager .

FROM registry.redhat.io/ubi9/ubi-minimal-pqc@sha256:8a842ac769de709143e4edeace516f2008dfdc431b64670ad3353fa323b44736

LABEL name="kserve-kueue-controller" \
      summary="Placeholder image for the KServe Kueue controller" \
      description="Placeholder image for the KServe Kueue controller."

COPY --from=builder /workspace/manager /manager

USER 1000:1000
ENTRYPOINT ["/manager"]
