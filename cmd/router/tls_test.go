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

package main

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func TestRouterTLSConfigDefaults(t *testing.T) {
	// Empty flags keep the Go serving defaults: TLS 1.2 minimum, no explicit
	// cipher restriction, and HTTP/1.1 only on ALPN because the router does
	// not serve HTTP/2.
	cfg, err := routerTLSConfig("", "", "")
	require.NoError(t, err)
	require.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
	require.Empty(t, cfg.CipherSuites)
	require.Empty(t, cfg.CurvePreferences)
	require.Equal(t, []string{"http/1.1"}, cfg.NextProtos)
}

func TestRouterTLSConfigAppliesProfileArgs(t *testing.T) {
	cfg, err := routerTLSConfig("VersionTLS12", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "")
	require.NoError(t, err)
	require.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
	require.Equal(t, []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}, cfg.CipherSuites)
	require.Equal(t, []string{"http/1.1"}, cfg.NextProtos)

	cfg, err = routerTLSConfig("VersionTLS13", "", "")
	require.NoError(t, err)
	require.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
	require.Empty(t, cfg.CipherSuites)
}

func TestRouterTLSConfigAppliesCurvePreferences(t *testing.T) {
	cfg, err := routerTLSConfig("VersionTLS13", "", "4588,29,23,24")
	require.NoError(t, err)
	require.Equal(t, []tls.CurveID{tls.X25519MLKEM768, tls.X25519, tls.CurveP256, tls.CurveP384}, cfg.CurvePreferences)
	require.Equal(t, []string{"http/1.1"}, cfg.NextProtos)
}

func TestRouterTLSConfigRejectsInvalidCurvePreferences(t *testing.T) {
	for _, value := range []string{"23,abc", "23,,29", "0", "-1"} {
		_, err := routerTLSConfig("VersionTLS13", "", value)
		require.Error(t, err, value)
	}
}

func TestRouterTLSConfigRejectsInvalidInput(t *testing.T) {
	_, err := routerTLSConfig("VersionTLS11", "", "")
	require.Error(t, err)

	_, err = routerTLSConfig("VersionTLS12", "TLS_NOT_A_CIPHER", "")
	require.Error(t, err)

	// crypto/tls does not permit configuring TLS 1.3 cipher suites.
	_, err = routerTLSConfig("VersionTLS13", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "")
	require.Error(t, err)

	// TLS 1.3 suite names are rejected below a TLS 1.3 floor.
	_, err = routerTLSConfig("VersionTLS12", "TLS_AES_128_GCM_SHA256", "")
	require.Error(t, err)

	_, err = routerTLSConfig("", "TLS_AES_128_GCM_SHA256", "")
	require.Error(t, err)
}

func TestRouterDisablesAutomaticHTTP2Setup(t *testing.T) {
	// The router is HTTP/1.1 only: the server must carry a non-nil, empty
	// TLSNextProto so net/http does not auto-configure HTTP/2. The automatic
	// setup appends h2 to the ALPN list and fails the listener when the
	// enforced cipher list has no HTTP/2-compatible AES-128-GCM suite.
	prevTls, prevTimeouts := *enableTlsFlag, routerTimeouts
	*enableTlsFlag = true
	sixty := int64(60)
	routerTimeouts = &v1alpha1.InfereceGraphRouterTimeouts{ServerRead: &sixty, ServerWrite: &sixty, ServerIdle: &sixty}
	defer func() {
		*enableTlsFlag = prevTls
		routerTimeouts = prevTimeouts
	}()

	server, err := newRouterServer()
	require.NoError(t, err)
	require.NotNil(t, server.TLSNextProto)
	require.Empty(t, server.TLSNextProto)
	require.Equal(t, []string{"http/1.1"}, server.TLSConfig.NextProtos)
}
