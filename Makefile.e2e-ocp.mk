# OpenShift E2E test targets for kserve midstream CI.
# Included from Makefile.overrides.mk.

E2E_MARKER ?= predictor
QUAY_REPO ?=
GITHUB_SHA ?= master

# Operator install mode: odh, rhoai, or empty (manual kustomize deploy).
OPERATOR_TYPE ?=
# Pin to a specific operator version (e.g. 3.4.0). When empty and CATALOG_SOURCE
# is an FBC fragment image, the version is auto-detected from the image tag.
OPERATOR_VERSION ?=
# FBC fragment image or CatalogSource name. Empty = default catalog.
# Example: quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.4
CATALOG_SOURCE ?=
# Set to false to skip copying local branch manifests into the operator PVC.
# Use false for "vanilla operator" testing with bundled images.
COPY_PR_MANIFESTS ?= true

build-images-ocp: ## Build and push KServe images for E2E testing. Requires QUAY_REPO.
	QUAY_REPO="$(QUAY_REPO)" GITHUB_SHA="$(GITHUB_SHA)" \
	./test/scripts/openshift-ci/build-kserve-images.sh

setup-e2e-ocp: ## Set up E2E test environment on OpenShift. Use OPERATOR_TYPE=odh|rhoai.
	OPERATOR_TYPE="$(strip $(OPERATOR_TYPE))" \
	OPERATOR_VERSION="$(strip $(OPERATOR_VERSION))" \
	CATALOG_SOURCE="$(strip $(CATALOG_SOURCE))" \
	COPY_PR_MANIFESTS="$(strip $(COPY_PR_MANIFESTS))" \
	./test/scripts/openshift-ci/setup-e2e-tests.sh "$(E2E_MARKER)"

e2e-ocp: ## Run E2E tests (assumes setup-e2e-ocp already ran).
	SETUP_E2E=false ./test/scripts/openshift-ci/run-e2e-tests.sh "$(E2E_MARKER)"

reset-e2e-ocp: ## Reset the test namespace for a fresh E2E rerun.
	./test/scripts/openshift-ci/setup-ci-namespace.sh

clean-setup-e2e-ocp: teardown-e2e-ocp setup-e2e-ocp ## Teardown then set up E2E environment (safe for operator switches).

teardown-e2e-ocp: ## Tear down the entire E2E environment (operator, DSC, namespaces).
	./test/scripts/openshift-ci/teardown-e2e-setup.sh
