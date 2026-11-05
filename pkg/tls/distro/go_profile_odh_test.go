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

package distro

import (
	"bytes"
	"testing"

	"github.com/go-logr/logr/funcr"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func goProfileReader(t *testing.T, adherence configv1.TLSAdherencePolicy, profile *configv1.TLSSecurityProfile) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, configv1.AddToScheme(scheme))
	return fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(&configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence:       adherence,
			TLSSecurityProfile: profile,
		},
	}).Build()
}

func TestInjectGoTLSArgs(t *testing.T) {
	customTLS12 := &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{
		TLSProfileSpec: configv1.TLSProfileSpec{
			MinTLSVersion: configv1.VersionTLS12,
			Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256", "ECDHE-RSA-AES256-GCM-SHA384"},
			Groups:        []configv1.TLSGroup{configv1.TLSGroupSecP256r1, configv1.TLSGroupX25519},
		},
	}}
	modern := &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType}
	oldProfile := &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType}
	aes256Only := &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{
		TLSProfileSpec: configv1.TLSProfileSpec{
			MinTLSVersion: configv1.VersionTLS12,
			Ciphers:       []string{"ECDHE-RSA-AES256-GCM-SHA384"},
		},
	}}

	tests := []struct {
		name      string
		reader    client.Reader
		wantArgs  []string
		wantError bool
	}{
		{
			name:   "strict custom TLS 1.2 profile projects version, cipher suites and curves",
			reader: goProfileReader(t, configv1.TLSAdherencePolicyStrictAllComponents, customTLS12),
			wantArgs: []string{
				"--tls-min-version", "VersionTLS12",
				"--tls-cipher-suites", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
				"--tls-curve-preferences", "23,29",
			},
		},
		{
			name:   "strict Modern profile omits cipher suites because TLS 1.3 manages them internally",
			reader: goProfileReader(t, configv1.TLSAdherencePolicyStrictAllComponents, modern),
			wantArgs: []string{
				"--tls-min-version", "VersionTLS13",
				"--tls-curve-preferences", "4588,29,23,24",
			},
		},
		{
			name:      "strict Old profile is rejected before rollout because Go workloads cannot serve it",
			reader:    goProfileReader(t, configv1.TLSAdherencePolicyStrictAllComponents, oldProfile),
			wantError: true,
		},
		{
			name:   "strict custom AES-256-only profile projects because the router serves HTTP/1.1 only",
			reader: goProfileReader(t, configv1.TLSAdherencePolicyStrictAllComponents, aes256Only),
			wantArgs: []string{
				"--tls-min-version", "VersionTLS12",
				"--tls-cipher-suites", "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
			},
		},
		{
			name:     "legacy adherence keeps workload defaults",
			reader:   goProfileReader(t, configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly, modern),
			wantArgs: nil,
		},
		{
			name:     "no opinion adherence keeps workload defaults",
			reader:   goProfileReader(t, configv1.TLSAdherencePolicyNoOpinion, modern),
			wantArgs: nil,
		},
		{
			name:   "unknown adherence is treated as strict",
			reader: goProfileReader(t, configv1.TLSAdherencePolicy("UnknownAdherence"), modern),
			wantArgs: []string{
				"--tls-min-version", "VersionTLS13",
				"--tls-curve-preferences", "4588,29,23,24",
			},
		},
		{
			name:   "nil APIServer profile resolves to the Intermediate profile",
			reader: goProfileReader(t, configv1.TLSAdherencePolicyStrictAllComponents, nil),
			wantArgs: []string{
				"--tls-min-version", "VersionTLS12",
				"--tls-cipher-suites", "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
				"--tls-curve-preferences", "4588,29,23,24",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			container := &corev1.Container{Name: "router", Args: []string{"--enable-tls"}}
			err := InjectGoTLSArgs(t.Context(), tt.reader, container)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, append([]string{"--enable-tls"}, tt.wantArgs...), container.Args)
		})
	}
}

func TestResolveWorkloadArgsLogsUnknownAdherence(t *testing.T) {
	var logs bytes.Buffer
	logger := funcr.New(func(prefix, args string) {
		logs.WriteString(prefix)
		logs.WriteString(args)
		logs.WriteString("\n")
	}, funcr.Options{})
	ctx := logf.IntoContext(t.Context(), logger)
	reader := goProfileReader(t, configv1.TLSAdherencePolicy("FuturePolicy"), &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType})

	_, honored, err := resolveWorkloadArgs(ctx, reader)
	require.NoError(t, err)
	require.True(t, honored)
	require.Contains(t, logs.String(), "Unknown TLS adherence policy")
}

func TestInjectGoTLSArgsAPIServerAvailability(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, configv1.AddToScheme(scheme))
	missing := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	noMatch := &apiMeta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "config.openshift.io", Kind: "APIServer"}}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}, "cluster", nil)

	tests := []struct {
		name      string
		reader    client.Reader
		wantError bool
	}{
		{name: "nil reader"},
		{name: "missing APIServer", reader: missing},
		{name: "unregistered APIServer", reader: getErrorReader{err: noMatch}},
		{name: "forbidden APIServer", reader: getErrorReader{err: forbidden}, wantError: true},
		{
			name:      "unexpected APIServer error",
			reader:    getErrorReader{err: apierrors.NewServiceUnavailable("APIServer read failed")},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			container := &corev1.Container{Name: "router", Args: []string{"--enable-tls"}}
			err := InjectGoTLSArgs(t.Context(), tt.reader, container)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, []string{"--enable-tls"}, container.Args)
		})
	}
}

func TestInjectGoTLSArgsToleratesNilContainer(t *testing.T) {
	require.NoError(t, InjectGoTLSArgs(t.Context(), nil, nil))
}
