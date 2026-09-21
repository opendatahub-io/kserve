# Placeholder Dockerfile for the KServe Kueue controller, to be replaced once ./cmd/kueue exists.

FROM registry.access.redhat.com/ubi9/go-toolset:1.25 AS builder

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

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

LABEL name="kserve-kueue-controller" \
      summary="Placeholder image for the KServe Kueue controller" \
      description="Placeholder image for the KServe Kueue controller."

COPY --from=builder /workspace/manager /manager

USER 1000:1000
ENTRYPOINT ["/manager"]
