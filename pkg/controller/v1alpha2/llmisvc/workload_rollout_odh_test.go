//go:build distro

package llmisvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

func TestODHPlatformDeploymentStrategy(t *testing.T) {
	one := int32(1)
	two := int32(2)
	svc := &v1alpha2.LLMInferenceService{}

	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &one}}
	applyPlatformDeploymentStrategy(svc, d)
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, d.Spec.Strategy.Type)

	d = &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &two}}
	applyPlatformDeploymentStrategy(svc, d)
	assert.Empty(t, d.Spec.Strategy.Type)

	d = &appsv1.Deployment{Spec: appsv1.DeploymentSpec{
		Replicas: &one,
		Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
	}}
	applyPlatformDeploymentStrategy(svc, d)
	assert.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, d.Spec.Strategy.Type)
}
