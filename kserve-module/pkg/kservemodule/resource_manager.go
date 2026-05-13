package kservemodule

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
)

var (
	kserveDeploymentsOCP = []string{
		kserveControllerDeployment,
		llmISVCControllerDeployment,
		odhModelControllerDeployment,
		// TODO: add localmodelControllerDeployment once localmodel manifests are included in OCP overlay
	}
	kserveDeploymentsXKS = []string{
		llmISVCControllerDeployment,
	}
)

func NewDeployer() *deploy.Deployer {
	return deploy.NewDeployer(
		deploy.WithFieldOwner(fieldOwner),
		deploy.WithApplyOrder(),
		deploy.WithCache(),
	)
}

func checkDeploymentReadiness(ctx context.Context, cli client.Client, namespace string, isXKS bool) error {
	var deployments []string
	if isXKS {
		deployments = append(deployments, kserveDeploymentsXKS...)
	} else {
		deployments = append(deployments, kserveDeploymentsOCP...)
	}

	var notReady []string
	for _, name := range deployments {
		dep := &appsv1.Deployment{}
		key := client.ObjectKey{Namespace: namespace, Name: name}
		if err := cli.Get(ctx, key, dep); err != nil {
			notReady = append(notReady, fmt.Sprintf("%s (get: %v)", name, err))
			continue
		}
		if dep.Status.AvailableReplicas < 1 {
			notReady = append(notReady, fmt.Sprintf("%s (availableReplicas=%d)", name, dep.Status.AvailableReplicas))
		}
	}

	if len(notReady) > 0 {
		return fmt.Errorf("deployments not ready: %s", strings.Join(notReady, ", "))
	}
	return nil
}
