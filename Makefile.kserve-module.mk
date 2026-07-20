KSERVE_MODULE_IMG ?= kserve-module-controller
PLATFORM ?= xks
E2E_IMG ?=

.PHONY: docker-build-kserve-module docker-push-kserve-module deploy-kserve-module \
	kustomize-build-kserve-module generate-kserve-module manifests-kserve-module \
	test-kserve-module setup-envtest-kserve-module precommit-km \
	e2e-setup-kserve-module e2e-cleanup-kserve-module e2e-kserve-module check-km


docker-build-kserve-module:
	${ENGINE} buildx build ${ARCH} --load \
		-t ${KO_DOCKER_REPO}/${KSERVE_MODULE_IMG}:${TAG} \
		-f kserve-module-controller.Dockerfile .

docker-push-kserve-module: docker-build-kserve-module
	${ENGINE} push ${KO_DOCKER_REPO}/${KSERVE_MODULE_IMG}:${TAG}

kustomize-build-kserve-module:
	$(KUSTOMIZE) build kserve-module/config

deploy-kserve-module:
	cd kserve-module/config && $(KUSTOMIZE) edit set image \
		kserve-module-controller=${KO_DOCKER_REPO}/${KSERVE_MODULE_IMG}:${TAG}
	$(KUSTOMIZE) build kserve-module/config | kubectl apply --server-side=true -f -

generate-kserve-module: controller-gen
	@$(CONTROLLER_GEN) object paths=./kserve-module/pkg/apis/v1alpha1/...

manifests-kserve-module: controller-gen
	@$(CONTROLLER_GEN) rbac:roleName=kserve-module-manager-role \
		paths=./kserve-module/pkg/kservemodule \
		output:rbac:artifacts:config=kserve-module/config/rbac
	@$(CONTROLLER_GEN) crd \
		paths=./kserve-module/pkg/apis/v1alpha1/... \
		output:crd:artifacts:config=kserve-module/config/crd

test-kserve-module: envtest
	cd kserve-module && \
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
	go test ./pkg/... -count=1

setup-envtest-kserve-module: envtest
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path

e2e-setup-kserve-module:
	bash kserve-module/tests/scripts/setup-cluster.sh --platform $(PLATFORM) \
		$(if $(E2E_IMG),--image $(E2E_IMG))

e2e-cleanup-kserve-module:
	bash kserve-module/tests/scripts/setup-cluster.sh --platform $(PLATFORM) --cleanup

e2e-kserve-module:
	cd kserve-module/tests/e2e && python -m pytest -v -x

precommit-km: fmt go-lint generate-kserve-module manifests-kserve-module test-kserve-module
	cd kserve-module && go mod tidy && go vet ./... && go build ./...

check-km: precommit-km
	@if [ -n "`git status -s kserve-module/`" ]; then \
		echo "ERROR: Git working tree is not clean after precommit-km (CI expects generated output to match the commit)."; \
		echo ""; \
		git status -s kserve-module/; \
		echo ""; \
		git diff --stat kserve-module/; \
		echo ""; \
		echo "Fix: make precommit-km && git add kserve-module/ && git commit --amend"; \
		exit 1; \
	fi
