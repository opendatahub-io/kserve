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

package tls

import (
	"context"
	"crypto/tls"
	"errors"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Resolve builds TLS option functions from the provided min version and cipher suites strings.
// When both are empty, the distro build reads the cluster TLS security profile from
// apiservers.config.openshift.io/cluster and applies the minimum TLS version from the profile.
// Falls back to TLS 1.2 if the cluster profile cannot be read.
func Resolve(ctx context.Context, cfg *rest.Config, tlsMinVersion, tlsCipherSuites string) ([]func(*tls.Config), error) {
	if tlsMinVersion != "" || tlsCipherSuites != "" {
		minVersion, err := parseMinVersion(tlsMinVersion)
		if err != nil {
			return nil, err
		}
		ciphers, err := parseCipherSuites(tlsCipherSuites)
		if err != nil {
			return nil, err
		}
		if minVersion >= tls.VersionTLS13 && len(ciphers) > 0 {
			return nil, errors.New("cipher suites cannot be configured with TLS 1.3 (Go manages TLS 1.3 ciphers internally)")
		}
		return tlsOptsFrom(minVersion, ciphers), nil
	}

	if cfg != nil {
		if minVersion, ok := clusterTLSMinVersion(ctx, cfg); ok {
			log.Info("Resolved TLS minimum version from cluster security profile", "minVersion", minVersion)
			return tlsOptsFrom(minVersion, nil), nil
		}
	}

	return tlsOptsFrom(tls.VersionTLS12, nil), nil
}

func clusterTLSMinVersion(ctx context.Context, cfg *rest.Config) (uint16, bool) {
	scheme := runtime.NewScheme()
	if err := configv1.Install(scheme); err != nil {
		log.V(1).Info("Unable to install OpenShift config scheme, falling back to TLS defaults", "err", err)
		return 0, false
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.V(1).Info("Unable to create cluster client for TLS profile lookup, falling back to TLS defaults", "err", err)
		return 0, false
	}

	apiServer := &configv1.APIServer{}
	if err := c.Get(ctx, types.NamespacedName{Name: "cluster"}, apiServer); err != nil {
		log.V(1).Info("Unable to read cluster APIServer config, falling back to TLS defaults", "err", err)
		return 0, false
	}

	profile := apiServer.Spec.TLSSecurityProfile
	if profile == nil {
		return tls.VersionTLS12, true
	}

	var ocpMinVersion configv1.TLSProtocolVersion
	switch profile.Type {
	case configv1.TLSProfileOldType:
		if p := configv1.TLSProfiles[configv1.TLSProfileOldType]; p != nil {
			ocpMinVersion = p.MinTLSVersion
		}
	case configv1.TLSProfileIntermediateType:
		if p := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]; p != nil {
			ocpMinVersion = p.MinTLSVersion
		}
	case configv1.TLSProfileModernType:
		if p := configv1.TLSProfiles[configv1.TLSProfileModernType]; p != nil {
			ocpMinVersion = p.MinTLSVersion
		}
	case configv1.TLSProfileCustomType:
		if profile.Custom != nil {
			ocpMinVersion = profile.Custom.MinTLSVersion
		}
	default:
		return tls.VersionTLS12, true
	}

	switch ocpMinVersion {
	case configv1.VersionTLS10, configv1.VersionTLS11:
		// Enforce minimum acceptable version regardless of cluster policy.
		log.Info("Cluster TLS profile min version is below TLS 1.2, enforcing TLS 1.2")
		return tls.VersionTLS12, true
	case configv1.VersionTLS12:
		return tls.VersionTLS12, true
	case configv1.VersionTLS13:
		return tls.VersionTLS13, true
	default:
		return tls.VersionTLS12, true
	}
}

func tlsOptsFrom(minVersion uint16, ciphers []uint16) []func(*tls.Config) {
	return []func(*tls.Config){
		func(c *tls.Config) {
			c.MinVersion = minVersion
			if len(ciphers) > 0 {
				c.CipherSuites = ciphers
			}
			c.NextProtos = []string{"h2", "http/1.1"}
		},
	}
}
