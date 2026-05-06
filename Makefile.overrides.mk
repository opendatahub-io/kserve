# Midstream-only Make targets for opendatahub-io/kserve.
# Loaded via `-include Makefile.overrides.mk` in the main Makefile.
# This file does not exist on upstream kserve/kserve.

# Enable distro build tag for platform-specific code.
# GOTAGS is picked up by the main Makefile to set GOFLAGS and --build-arg for Docker.
GOTAGS = distro
export GOFLAGS += -tags=$(GOTAGS)

.PHONY: deploy-dev-llm deploy-dev-llm-ocp deploy-ci uv-update-lockfiles

deploy-dev-llm:
	./hack/deploy_dev_llm.sh

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

KSERVE_MODULE_IMG ?= kserve-module-controller

.PHONY: docker-build-kserve-module docker-push-kserve-module deploy-kserve-module

docker-build-kserve-module:
	${ENGINE} buildx build ${ARCH} --load \
		--build-arg CMD=kserve-module \
		--build-arg YQ_VERSION=${YQ_VERSION} \
		-t ${KO_DOCKER_REPO}/${KSERVE_MODULE_IMG}:${TAG} \
		-f kserve-module-controller.Dockerfile .

docker-push-kserve-module: docker-build-kserve-module
	${ENGINE} push ${KO_DOCKER_REPO}/${KSERVE_MODULE_IMG}:${TAG}

KSERVE_MODULE_NS ?= opendatahub

deploy-kserve-module:
	@kubectl get namespace $(KSERVE_MODULE_NS) >/dev/null 2>&1 || kubectl create namespace $(KSERVE_MODULE_NS)
	cd config/kserve-module && $(KUSTOMIZE) edit set namespace $(KSERVE_MODULE_NS) && \
		$(KUSTOMIZE) edit set image \
		kserve-module-controller=${KO_DOCKER_REPO}/${KSERVE_MODULE_IMG}:${TAG}
	$(KUSTOMIZE) build config/kserve-module | kubectl apply --server-side=true -f -

# --- kserve-module: controller-gen targets ---

.PHONY: generate-kserve-module manifests-kserve-module test-kserve-module

generate-kserve-module: controller-gen
	@$(CONTROLLER_GEN) object paths=./pkg/apis/platform/...

manifests-kserve-module: controller-gen
	@$(CONTROLLER_GEN) rbac:roleName=kserve-module-manager-role \
		paths=./pkg/controller/kservemodule \
		output:rbac:artifacts:config=config/kserve-module/rbac
	@$(CONTROLLER_GEN) crd \
		paths=./pkg/apis/platform/... \
		output:crd:artifacts:config=config/kserve-module

test-kserve-module: envtest
	KUBEBUILDER_ASSETS=$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path) \
		go test ./pkg/controller/kservemodule/ ./pkg/apis/platform/... \
		-v -count=1 -race

# --- end kserve-module ---

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
