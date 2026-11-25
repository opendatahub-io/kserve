//go:build distro

/*
Copyright 2026 The KServe Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
)

func TestDefaultODHCustomCABundle(t *testing.T) {
	const customMountPath = "/custom/certs"

	scenarios := map[string]struct {
		args         []string
		env          []corev1.EnvVar
		volumes      []corev1.Volume
		volumeMounts []corev1.VolumeMount
		mountPath    string
		wantMount    string // empty: the pod spec must stay unchanged
	}{
		"https source": {
			args:      []string{"https://models.example.test/model.tar", "/mnt/models"},
			wantMount: constants.DefaultCaBundleVolumeMountPath,
		},
		"hf source": {
			args:      []string{"hf://org/model", "/mnt/models"},
			wantMount: constants.DefaultCaBundleVolumeMountPath,
		},
		"several hf sources": {
			args:      []string{"hf://org/model", "/mnt/models", "hf://org/adapter", "/mnt/models/lora/a"},
			wantMount: constants.DefaultCaBundleVolumeMountPath,
		},
		"configured mount path": {
			args:      []string{"hf://org/model", "/mnt/models"},
			mountPath: customMountPath,
			wantMount: customMountPath,
		},
		"s3 credentials naming the same bundle": {
			args: []string{"hf://org/model", "/mnt/models"},
			env: []corev1.EnvVar{
				{Name: awsCABundleConfigMapEnvVar, Value: constants.ODHKServeCustomCABundleConfigMapName},
				{Name: awsCABundleEnvVar, Value: "/etc/ssl/custom-certs/cabundle.crt"},
			},
			wantMount: constants.DefaultCaBundleVolumeMountPath,
		},
		"s3 source": {
			args: []string{"s3://bucket/model", "/mnt/models"},
		},
		"gs source": {
			args: []string{"gs://bucket/model", "/mnt/models"},
		},
		"plain http source": {
			args: []string{"http://models.example.test/model.tar", "/mnt/models"},
		},
		"hf and s3 sources": {
			args: []string{"hf://org/model", "/mnt/models", "s3://bucket/adapter", "/mnt/models/lora/a"},
		},
		"no source": {
			args: []string{"/mnt/models"},
		},
		"bundle ConfigMap already set": {
			args: []string{"hf://org/model", "/mnt/models"},
			env:  []corev1.EnvVar{{Name: constants.CaBundleConfigMapNameEnvVarKey, Value: "global-ca-bundle"}},
		},
		"bundle mount point already set": {
			args: []string{"hf://org/model", "/mnt/models"},
			env:  []corev1.EnvVar{{Name: constants.CaBundleVolumeMountPathEnvVarKey, Value: customMountPath}},
		},
		"REQUESTS_CA_BUNDLE already set": {
			args: []string{"https://models.example.test/model.tar", "/mnt/models"},
			env:  []corev1.EnvVar{{Name: requestsCABundleEnvVar, Value: "/certs/ca.crt"}},
		},
		"SSL_CERT_FILE already set": {
			args: []string{"https://models.example.test/model.tar", "/mnt/models"},
			env:  []corev1.EnvVar{{Name: sslCertFileEnvVar, Value: "/certs/ca.crt"}},
		},
		"s3 credentials naming another bundle": {
			args: []string{"hf://org/model", "/mnt/models"},
			env:  []corev1.EnvVar{{Name: awsCABundleConfigMapEnvVar, Value: "my-s3-ca"}},
		},
		"s3 bundle path without a ConfigMap": {
			args: []string{"hf://org/model", "/mnt/models"},
			env:  []corev1.EnvVar{{Name: awsCABundleEnvVar, Value: "/certs/ca.crt"}},
		},
		"volume name taken": {
			args:    []string{"hf://org/model", "/mnt/models"},
			volumes: []corev1.Volume{{Name: odhCABundleVolumeName}},
		},
		"mount path taken": {
			args:         []string{"hf://org/model", "/mnt/models"},
			volumeMounts: []corev1.VolumeMount{{Name: "certs", MountPath: constants.DefaultCaBundleVolumeMountPath}},
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			g := gomega.NewGomegaWithT(t)
			podSpec := &corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Name:         constants.StorageInitializerContainerName,
					Args:         scenario.args,
					Env:          scenario.env,
					VolumeMounts: scenario.volumeMounts,
				}},
				Volumes: scenario.volumes,
			}
			original := podSpec.DeepCopy()
			initContainer := &podSpec.InitContainers[0]

			changed := DefaultODHCustomCABundle(podSpec, initContainer, scenario.mountPath)

			if scenario.wantMount == "" {
				g.Expect(changed).To(gomega.BeFalse())
				g.Expect(podSpec).To(gomega.Equal(original))
				return
			}

			g.Expect(changed).To(gomega.BeTrue())
			g.Expect(podSpec.Volumes).To(gomega.HaveLen(len(original.Volumes) + 1))
			volume := podSpec.Volumes[len(podSpec.Volumes)-1]
			g.Expect(volume.Name).To(gomega.Equal(odhCABundleVolumeName))
			g.Expect(volume.ConfigMap).NotTo(gomega.BeNil())
			g.Expect(volume.ConfigMap.Name).To(gomega.Equal(constants.ODHKServeCustomCABundleConfigMapName))
			g.Expect(volume.ConfigMap.Optional).To(gomega.HaveValue(gomega.BeTrue()), "a namespace without the bundle must still start the pod")
			g.Expect(initContainer.VolumeMounts).To(gomega.ContainElement(corev1.VolumeMount{
				Name: odhCABundleVolumeName, MountPath: scenario.wantMount, ReadOnly: true,
			}))
			g.Expect(initContainer.Env).To(gomega.ContainElements(
				corev1.EnvVar{Name: constants.CaBundleConfigMapNameEnvVarKey, Value: constants.ODHKServeCustomCABundleConfigMapName},
				corev1.EnvVar{Name: constants.CaBundleVolumeMountPathEnvVarKey, Value: scenario.wantMount},
			))

			// Applying the default again must not add a second volume or mount.
			applied := podSpec.DeepCopy()
			g.Expect(DefaultODHCustomCABundle(podSpec, initContainer, scenario.mountPath)).To(gomega.BeFalse())
			g.Expect(podSpec).To(gomega.Equal(applied))
		})
	}
}

func TestDefaultODHCustomCABundleNilInputs(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	g.Expect(DefaultODHCustomCABundle(nil, &corev1.Container{Args: []string{"hf://org/model"}}, "")).To(gomega.BeFalse())
	g.Expect(DefaultODHCustomCABundle(&corev1.PodSpec{}, nil, "")).To(gomega.BeFalse())
}
