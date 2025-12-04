# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

This is a fork of KServe maintained by OpenDataHub (opendatahub-io/kserve), which tracks upstream kserve/kserve. KServe provides Kubernetes CRDs for serving ML models with features like autoscaling, canary deployments, and multi-framework support.

### Remote Setup

This repository typically uses three remotes:
- `origin`: Your personal fork (${GH_USER}/kserve)
- `odh`: OpenDataHub fork (opendatahub-io/kserve) - default branch: `master`
- `upstream`: Upstream KServe (kserve/kserve) - used as `kserve` or `upstream`

When contributing, be aware of the base branch depending on target repository.

## Build System

### Prerequisites

The build system uses Go, Python, and various tooling defined in `Makefile.tools.mk`. Dependencies are managed through:
- Go modules (`go.mod`, `go.sum`)
- Python uv lock files (`python/*/uv.lock`)
- Tool versions in `kserve-deps.env`

### Essential Commands

**Code Generation & Manifests:**
```bash
make manifests          # Generate CRDs, RBAC from Go types
make generate           # Run all code generation (clients, OpenAPI, Python SDK)
make precommit          # Run all checks before committing (sync-deps, vet, lint, generate, manifests, uv-lock)
```

**Testing:**
```bash
make test               # Run Go unit tests with coverage (requires envtest)
make test-qpext         # Run qpext-specific tests
make setup-envtest      # Setup Kubernetes test binaries for envtest

# Run single test package:
KUBEBUILDER_ASSETS="$(./bin/setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" go test ./pkg/controller/v1alpha1/llmisvc -v

# Run specific test:
KUBEBUILDER_ASSETS="$(./bin/setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" go test ./pkg/controller/v1alpha1/llmisvc -run TestControllerReconcile -v
```

**Linting & Formatting:**
```bash
make fmt                # Format Go code
make py-fmt             # Format Python code with black
make go-lint            # Lint Go code with golangci-lint
make py-lint            # Lint Python with flake8
make vet                # Run go vet
```

**Development:**
```bash
make manager            # Build controller manager binary
make agent              # Build agent binary
make router             # Build router binary

make deploy-dev         # Deploy to k8s cluster using development overlay
make undeploy-dev       # Remove development deployment
make deploy-helm        # Deploy using Helm charts (USE_LOCAL_CHARTS=true)
```

**Docker Images:**
```bash
# Main controller
make docker-build-llmisvc
make docker-push-llmisvc

# Other images
make docker-build-sklearn
make docker-push-storageInitializer
# See Makefile for full list of image targets
```

**Dependency Management:**
```bash
make sync-deps          # Sync dependency versions from go.mod to kserve-deps.env
make uv-lock            # Update Python uv.lock files
make tidy               # Run go mod tidy
```

**Utilities:**
```bash
make clean              # Remove all installed binaries (useful when targets fail)
make check              # Verify precommit checks pass (used in CI)
```

## Architecture

### Core Components

**Controllers (pkg/controller/):**
- `v1beta1/`: Main KServe controller for InferenceService CRD
- `v1alpha1/llmisvc/`: LLM-specific controller for disaggregated serving
  - Supports single-node and multi-node (prefill/decode) architectures
  - Manages workloads via Deployments or LeaderWorkerSets
  - Handles router/scheduler for request routing
- `v1alpha1/inferencegraph/`: Orchestrates multi-model pipelines
- `v1alpha1/trainedmodel/`, `v1alpha1/localmodel/`: Model management

**API Types (pkg/apis/serving/):**
- `v1beta1/`: InferenceService, ServingRuntime, ClusterServingRuntime
- `v1alpha1/`: LLMInferenceService, LLMInferenceServiceConfig, InferenceGraph, TrainedModel

**Command Entrypoints (cmd/):**
- `manager/`: Main KServe controller
- `llmisvc/`: LLM inference service controller
- `agent/`: Model agent for sidecar pattern
- `router/`: Request router component

**Python SDK & Servers (python/):**
- `kserve/`: Core Python SDK
- `*server/`: Framework-specific servers (sklearn, xgboost, huggingface, etc.)
- `storage/`, `storage-initializer/`: Model loading from storage

### LLMInferenceService Architecture

LLMInferenceService supports two architectures:

1. **Single-node**: One deployment serving entire model
2. **Disaggregated (prefill/decode)**: Separate deployments for prompt processing and token generation

Key reconciliation logic in `pkg/controller/v1alpha1/llmisvc/`:
- `controller.go`: Main reconciliation loop
- `workload_single_node.go`, `workload_multi_node.go`: Workload creation
- `router.go`, `scheduler.go`: Networking and request routing
- `config_merge.go`, `config_loader.go`: Config template merging
- `validation/`: Webhook validation

### Configuration Management

**Kustomize overlays (config/):**
- `config/default/`: Base kustomize configuration
- `config/overlays/development/`: Dev environment
- `config/overlays/odh/`: OpenDataHub-specific customizations
- `config/llmisvc/`: LLM controller-specific configs
- `config/llmisvcconfig/`: LLM config templates (router, scheduler, workers)

**CRD Generation:**
The `make manifests` target:
1. Generates CRDs from Go types using controller-gen
2. Moves LLMISVC CRDs to separate folders (`config/crd/full/llmisvc/`)
3. Removes certain validations for runtime template injection
4. Copies to Helm chart templates
5. Generates minimal CRDs for limited cluster permissions

### Client Code Generation

Generated code in `pkg/client/` is auto-generated from API types:
- Clientset, informers, listers for typed access
- Run `make generate` to regenerate after API changes
- Uses k8s.io/code-generator via `hack/update-codegen.sh`

### Testing Structure

**Unit/Integration Tests:**
- Controller tests use envtest (local Kubernetes API)
- Fixtures in `pkg/controller/*/fixture/`
- Test builders pattern for creating test objects

**E2E Tests (test/e2e/):**
- Python-based tests using pytest
- `test/e2e/llmisvc/`: LLM-specific E2E tests
- Other directories for feature-specific tests (predictor, transformer, etc.)

## Key Workflows

### Adding a New API Field

1. Modify API types in `pkg/apis/serving/v1alpha1/` or `v1beta1/`
2. Run `make generate` to update generated code
3. Run `make manifests` to update CRDs
4. Update controller reconciliation logic
5. Add validation in webhook if needed
6. Update tests
7. Run `make precommit` before committing

### Working with LLMInferenceService

When modifying LLM controller:
- Config merging happens in `config_merge.go` - merges LLMInferenceServiceConfig with LLMInferenceService
- Workload creation split by architecture (single vs multi-node)
- Router uses Gateway API (HTTPRoute) for traffic management
- Scheduler coordinates prefill/decode in disaggregated mode

### Syncing from Upstream

When syncing specific components from upstream (kserve/kserve):
1. Fetch upstream: `git fetch upstream`
2. Use selective checkout for specific paths:
   ```bash
   git checkout upstream/master -- path/to/files
   ```
3. For llmisvc files, search both filename and content references
4. Common paths: `config/llmisvc/`, `pkg/controller/v1alpha1/llmisvc/`, `charts/kserve-llmisvc-*`

### Deploying for Development

```bash
# Option 1: Direct kustomize deployment
make deploy-dev

# Option 2: Helm charts from local
USE_LOCAL_CHARTS=true ./hack/setup/infra/manage.kserve-helm.sh

# For self-signed certs (useful in dev):
KSERVE_ENABLE_SELF_SIGNED_CA=true make deploy-dev
```

## Important Notes

### CRD Size Management

The InferenceService CRD can be very large. The build removes:
- `ephemeralContainers` properties
- Various required fields on probes
- Sets protocol defaults to "TCP"

See the extensive `yq` processing in the `manifests` target (Makefile:147-160).

### LLMISVC Validation Removal

LLMInferenceServiceConfig CRDs have validations removed to allow Go template injection at runtime. This enables dynamic config merging. See Makefile:124-139.

### Envtest Setup

If tests fail with missing binaries, run `make setup-envtest` or `make clean` then retry.

### Python Environment

Python packages use uv for dependency management. Each package has its own `uv.lock` file. Run `make uv-lock` after changing Python dependencies.

### Image Building

Set `KO_DOCKER_REPO` environment variable to your registry:
```bash
export KO_DOCKER_REPO=quay.io/your-username
```

Use `ENGINE=podman` if using podman instead of docker.

## File Organization

```
kserve/
├── cmd/                    # Binary entrypoints
├── pkg/
│   ├── apis/serving/       # CRD API types (v1alpha1, v1beta1)
│   ├── controller/         # Controller implementations
│   ├── client/             # Generated Kubernetes clients
│   ├── webhook/            # Admission webhooks
│   └── utils/              # Shared utilities
├── config/                 # Kustomize manifests and CRDs
│   ├── crd/                # Generated CRDs (full and minimal)
│   ├── rbac/               # RBAC resources
│   ├── llmisvc/            # LLM controller configs
│   └── overlays/           # Environment-specific overlays
├── python/                 # Python SDK and servers
│   ├── kserve/             # Core Python SDK
│   └── *server/            # Framework-specific servers
├── test/
│   ├── e2e/                # End-to-end Python tests
│   └── crds/               # Consolidated test CRDs
├── charts/                 # Helm charts
│   ├── kserve-crd/         # Main CRDs chart
│   ├── kserve-resources/   # Controller and webhooks
│   └── kserve-llmisvc-*/   # LLM-specific charts
└── hack/                   # Scripts for codegen and setup
```
