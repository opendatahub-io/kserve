/*
Copyright 2022 The KServe Authors.

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

package s3

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
)

func TestBuildODHS3EnvVars(t *testing.T) {
	scenarios := map[string]struct {
		annotations map[string]string
		secret      *map[string][]byte
		config      S3Config
		expected    []corev1.EnvVar
	}{
		"ODHS3Endpoint": {
			annotations: map[string]string{
				InferenceServiceS3SecretEndpointAnnotation: "s3.aws.com",
			},
			secret: &map[string][]byte{
				constants.ODHS3Endpoint: []byte("odh-s3.aws.com"),
			},
			expected: []corev1.EnvVar{
				{
					Name:  S3Endpoint,
					Value: "odh-s3.aws.com",
				},
				{
					Name:  AWSEndpointUrl,
					Value: "https://odh-s3.aws.com",
				},
			},
		},
		"ODHS3EndpointWithHttpsProtocol": {
			annotations: map[string]string{
				InferenceServiceS3SecretEndpointAnnotation: "https://s3.aws.com",
			},
			secret: &map[string][]byte{
				constants.ODHS3Endpoint: []byte("https://odh-s3.aws.com"),
				S3UseHttps:              []byte("0"),
				S3VerifySSL:             []byte("1"),
			},
			expected: []corev1.EnvVar{
				{
					Name:  S3UseHttps,
					Value: "1",
				},
				{
					Name:  S3Endpoint,
					Value: "odh-s3.aws.com",
				},
				{
					Name:  AWSEndpointUrl,
					Value: "https://odh-s3.aws.com",
				},
				{
					Name:  S3VerifySSL,
					Value: "1",
				},
			},
		},
		"ODHS3EndpointWithHttpProtocol": {
			annotations: map[string]string{
				InferenceServiceS3SecretEndpointAnnotation: "http://s3.aws.com",
			},
			secret: &map[string][]byte{
				constants.ODHS3Endpoint: []byte("http://odh-s3.aws.com"),
				S3UseHttps:              []byte("1"),
				S3VerifySSL:             []byte("0"),
			},
			expected: []corev1.EnvVar{
				{
					Name:  S3UseHttps,
					Value: "0",
				},
				{
					Name:  S3Endpoint,
					Value: "odh-s3.aws.com",
				},
				{
					Name:  AWSEndpointUrl,
					Value: "http://odh-s3.aws.com",
				},
				{
					Name:  S3VerifySSL,
					Value: "0",
				},
			},
		},
	}
	for name, scenario := range scenarios {
		envs := BuildS3EnvVars(scenario.annotations, scenario.secret, &scenario.config)

		if diff := cmp.Diff(scenario.expected, envs); diff != "" {
			t.Errorf("Test %q unexpected result (-want +got): %v", name, diff)
		}
	}
}
