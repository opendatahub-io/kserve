KSERVE_MODULE_IMG ?= kserve-module-controller

.PHONY: docker-build-kserve-module docker-push-kserve-module deploy-kserve-module \
	kustomize-build-kserve-module generate-kserve-module manifests-kserve-module \
	test-kserve-module setup-envtest-kserve-module

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
	go test ./pkg/... -v -count=1

setup-envtest-kserve-module: envtest
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path
