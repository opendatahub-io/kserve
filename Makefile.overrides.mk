# Midstream-only Make targets for opendatahub-io/kserve.
# Loaded via `-include Makefile.overrides.mk` in the main Makefile.
# This file does not exist on upstream kserve/kserve.

# UBI base image for Python Dockerfiles (upstream defaults to python:3.11-slim-bookworm).
BASE_IMG = registry.access.redhat.com/ubi9/python-311:9.7

# Enable distro build tag for platform-specific code.
# GOTAGS is picked up by the main Makefile to set GOFLAGS and --build-arg for Docker.
GOTAGS = distro
export GOFLAGS += -tags=$(GOTAGS)

# Align image names with ODH registry conventions so that `make docker-build-*`
# produces images that match CI expectations without re-tagging.
AGENT_IMG = kserve-agent
ROUTER_IMG = kserve-router
STORAGE_INIT_IMG = kserve-storage-initializer

.PHONY: deploy-dev-llm-ocp deploy-ci uv-update-lockfiles build-images-ocp setup-e2e-ocp e2e-ocp reset-e2e-ocp teardown-e2e-ocp

deploy-dev-llm-ocp:
	./test/scripts/openshift-ci/setup-llm.sh --deploy-kuadrant

deploy-ci: manifests
	kubectl apply --server-side=true --force-conflicts -k config/crd/full
	kubectl apply --server-side=true --force-conflicts -k config/crd/full/localmodel
	kubectl apply --server-side=true --force-conflicts -k config/crd/full/llmisvc
	kubectl wait --for=condition=established --timeout=60s crd/llminferenceserviceconfigs.serving.kserve.io
	kubectl apply --server-side=true -k config/overlays/test
	kubectl wait --for=condition=ready pod -l control-plane=kserve-controller-manager -n kserve --timeout=300s
	kubectl apply --server-side=true -k config/overlays/test/clusterresources

uv-update-lockfiles:
	bash -ec 'for value in $$(find . -name uv.lock -exec dirname {} \;); do (cd "$${value}" && echo "Updating $${value}/uv.lock" && uv update --lock); done'

E2E_MARKER ?= predictor
QUAY_REPO ?=
GITHUB_SHA ?= master

# Operator install mode: odh, rhoai, or empty (manual kustomize deploy).
OPERATOR_TYPE ?=
# Pin to a specific operator version (e.g. 3.4.0). Empty = latest in channel.
OPERATOR_VERSION ?=
# FBC fragment image or CatalogSource name. Empty = default catalog.
# Example: quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.4
CATALOG_SOURCE ?=

build-images-ocp: ## Build and push KServe images for E2E testing. Requires QUAY_REPO.
	QUAY_REPO="$(QUAY_REPO)" GITHUB_SHA="$(GITHUB_SHA)" \
	./test/scripts/openshift-ci/build-kserve-images.sh

setup-e2e-ocp: ## Set up E2E test environment on OpenShift. Use OPERATOR_TYPE=odh|rhoai.
	INSTALL_ODH_OPERATOR=$$([ -n "$(OPERATOR_TYPE)" ] && echo true || echo false) \
	OPERATOR_TYPE="$(OPERATOR_TYPE)" \
	OPERATOR_VERSION="$(OPERATOR_VERSION)" \
	CATALOG_SOURCE="$(CATALOG_SOURCE)" \
	./test/scripts/openshift-ci/setup-e2e-tests.sh "$(E2E_MARKER)"

e2e-ocp:
	./test/scripts/openshift-ci/run-e2e-tests.sh "$(E2E_MARKER)"

reset-e2e-ocp: ## Reset the test namespace for a fresh E2E rerun.
	./test/scripts/openshift-ci/setup-ci-namespace.sh

teardown-e2e-ocp:
	./test/scripts/openshift-ci/teardown-e2e-setup.sh "$(E2E_MARKER)"

manifests-distro: controller-gen
	@$(CONTROLLER_GEN) rbac:roleName=kserve-llmisvc-distro-role \
		paths=./pkg/controller/v1alpha2/llmisvc/distro \
		output:rbac:artifacts:config=config/overlays/odh/rbac/llmisvc
	@$(CONTROLLER_GEN) rbac:roleName=kserve-localmodel-distro-role \
		paths=./pkg/controller/v1alpha1/localmodel/distro \
		output:rbac:artifacts:config=config/overlays/odh-modelcache/rbac/localmodel
	@$(CONTROLLER_GEN) rbac:roleName=kserve-localmodelnode-distro-role \
		paths=./pkg/controller/v1alpha1/localmodelnode/distro \
		output:rbac:artifacts:config=config/overlays/odh-modelcache/rbac/localmodelnode

-include Makefile.kserve-module.mk

