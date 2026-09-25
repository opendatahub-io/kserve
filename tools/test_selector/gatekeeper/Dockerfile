# syntax=docker/dockerfile:1

# Match the repository's Go directive (go 1.26.7) with a public base image so
# the gatekeeper can be built identically by developers and OpenShift CI. The
# evaluator runs go list in the merged worktree, so its cache lives in /tmp.
FROM golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514

USER 0

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        jq \
        openssl \
        python3.11 \
        python3-pip \
    && rm -rf /var/lib/apt/lists/* \
    && ln -sf /usr/bin/python3.11 /usr/local/bin/python

# Install dependencies outside the source tree.  The image has no credential,
# token, or private key; the dispatcher receives those only through its CI
# secret mount at runtime.
COPY tools/test_selector/gatekeeper/requirements-gatekeeper.txt /tmp/requirements-gatekeeper.txt
RUN python3.11 -m pip install --no-cache-dir --disable-pip-version-check \
        --target /opt/python \
        -r /tmp/requirements-gatekeeper.txt \
    && rm -f /tmp/requirements-gatekeeper.txt

# Copy only the trusted selector package and gatekeeper inputs.  In particular,
# no generated mapping is copied from a checkout: learn creates it under the
# evaluator-owned /tmp workspace.
COPY tools/test_selector/__init__.py \
     tools/test_selector/__main__.py \
     tools/test_selector/cli.py \
     tools/test_selector/config.json \
     tools/test_selector/config_loader.py \
     /opt/test-selector/test_selector/
COPY tools/test_selector/analyzers /opt/test-selector/test_selector/analyzers
COPY tools/test_selector/graph /opt/test-selector/test_selector/graph
COPY tools/test_selector/mapping /opt/test-selector/test_selector/mapping
COPY tools/test_selector/output /opt/test-selector/test_selector/output
COPY tools/test_selector/selector /opt/test-selector/test_selector/selector
COPY tools/test_selector/gatekeeper/__init__.py \
     tools/test_selector/gatekeeper/dispatch.py \
     tools/test_selector/gatekeeper/evaluate.py \
     tools/test_selector/gatekeeper/ocp_jobs.json \
     tools/test_selector/gatekeeper/selection.schema.json \
     /opt/test-selector/test_selector/gatekeeper/

ENV PYTHONPATH="/opt/test-selector:/opt/python" \
    PYTHONSAFEPATH="1" \
    PYTHONDONTWRITEBYTECODE="1" \
    PYTHONUNBUFFERED="1" \
    HOME="/tmp" \
    XDG_CACHE_HOME="/tmp" \
    GOCACHE="/tmp/go-build" \
    GOMODCACHE="/tmp/go-mod" \
    GOFLAGS="-buildvcs=false" \
    GOTOOLCHAIN="local" \
    GIT_TERMINAL_PROMPT="0"

WORKDIR /opt

# The evaluator is the safe default.  The dispatcher step overrides the
# module argument and receives its credential through the CI secret mount.
ENTRYPOINT ["python3.11"]
CMD ["-m", "test_selector.gatekeeper.evaluate"]

# OpenShift may run this image with an arbitrary non-root UID.  All packaged
# files are read-only to that UID and the only writable location is /tmp.
USER 1001
