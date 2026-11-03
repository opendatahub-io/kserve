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
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kservetls "github.com/kserve/kserve/pkg/tls"
)

var log = ctrl.Log.WithName("tls")

// openSSLToIANACipher converts OpenShift profile cipher names (OpenSSL style) to
// IANA names for lookup in the runtime-supported cipher tables. TLS 1.3 suite
// names are already IANA and resolve without an alias.
var openSSLToIANACipher = map[string]string{
	"ECDHE-ECDSA-AES128-GCM-SHA256":        "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
	"ECDHE-RSA-AES128-GCM-SHA256":          "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
	"ECDHE-ECDSA-AES256-GCM-SHA384":        "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
	"ECDHE-RSA-AES256-GCM-SHA384":          "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
	"ECDHE-ECDSA-CHACHA20-POLY1305":        "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256",
	"ECDHE-RSA-CHACHA20-POLY1305":          "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
	"ECDHE-ECDSA-CHACHA20-POLY1305-SHA256": "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256",
	"ECDHE-RSA-CHACHA20-POLY1305-SHA256":   "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
	"ECDHE-ECDSA-AES128-SHA256":            "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256",
	"ECDHE-RSA-AES128-SHA256":              "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256",
	"ECDHE-ECDSA-AES128-SHA":               "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA",
	"ECDHE-RSA-AES128-SHA":                 "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA",
	"ECDHE-ECDSA-AES256-SHA":               "TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA",
	"ECDHE-RSA-AES256-SHA":                 "TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA",
	"AES128-GCM-SHA256":                    "TLS_RSA_WITH_AES_128_GCM_SHA256",
	"AES256-GCM-SHA384":                    "TLS_RSA_WITH_AES_256_GCM_SHA384",
	"AES128-SHA256":                        "TLS_RSA_WITH_AES_128_CBC_SHA256",
	"AES128-SHA":                           "TLS_RSA_WITH_AES_128_CBC_SHA",
	"AES256-SHA":                           "TLS_RSA_WITH_AES_256_CBC_SHA",
	"DES-CBC3-SHA":                         "TLS_RSA_WITH_3DES_EDE_CBC_SHA",
}

// goCipherSuites is every cipher the runtime can negotiate, keyed by IANA name.
// RC4 is excluded because RFC 7465 prohibits it.
var goCipherSuites = func() map[string]uint16 {
	suites := make(map[string]uint16)
	for _, cs := range tls.CipherSuites() {
		suites[cs.Name] = cs.ID
	}
	for _, cs := range tls.InsecureCipherSuites() {
		if !strings.Contains(cs.Name, "RC4") {
			suites[cs.Name] = cs.ID
		}
	}
	return suites
}()

var openSSLToGoCipher = func() map[string]uint16 {
	result := make(map[string]uint16, len(goCipherSuites)+len(openSSLToIANACipher))
	for name, id := range goCipherSuites {
		result[name] = id
	}
	for openSSL, iana := range openSSLToIANACipher {
		if id, ok := goCipherSuites[iana]; ok {
			result[openSSL] = id
		}
	}
	return result
}()

// goCipherID resolves a profile cipher name to a Go cipher ID usable at minVersion.
// TLS 1.3-only suites (0x1300-0x13ff) cannot be negotiated for TLS <= 1.2 handshakes.
func goCipherID(name string, minVersion uint16) (uint16, bool) {
	if iana, ok := openSSLToIANACipher[name]; ok {
		name = iana
	}
	id, ok := goCipherSuites[name]
	if !ok {
		return 0, false
	}
	if minVersion < tls.VersionTLS13 && id >= 0x1300 && id <= 0x13ff {
		return 0, false
	}
	return id, true
}

var intermediateCiphers = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

var ocpTLSVersionMap = map[configv1.TLSProtocolVersion]uint16{
	"VersionTLS10": tls.VersionTLS10,
	"VersionTLS11": tls.VersionTLS11,
	"VersionTLS12": tls.VersionTLS12,
	"VersionTLS13": tls.VersionTLS13,
}

const apiServerName = "cluster"

// Resolve builds TLS option functions from the provided min version and cipher suites strings.
// When both are empty, reads the cluster TLS security profile and adherence policy from
// apiservers.config.openshift.io/cluster. Falls back to hardened Intermediate defaults
// when the profile is unavailable (non-OpenShift cluster or transient error).
func Resolve(ctx context.Context, cfg *rest.Config, tlsMinVersion, tlsCipherSuites string) (Result, error) {
	if tlsMinVersion != "" || tlsCipherSuites != "" {
		tlsOpts, err := kservetls.Resolve(tlsMinVersion, tlsCipherSuites)
		if err != nil {
			return Result{}, err
		}
		return Result{TLSOpts: tlsOpts}, nil
	}

	if cfg == nil {
		return Result{TLSOpts: tlsOptsFrom(tls.VersionTLS12, nil)}, nil
	}

	resolveCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	scheme := runtime.NewScheme()
	if err := configv1.Install(scheme); err != nil {
		log.Info("Unable to install OpenShift config scheme, using hardened defaults", "error", err)
		return intermediateResult(false, false), nil
	}

	// Bound the bootstrap client's requests, including REST mapper discovery,
	// which the resolve context does not cover.
	bootstrapCfg := rest.CopyConfig(cfg)
	bootstrapCfg.Timeout = 10 * time.Second
	k8sClient, err := client.New(bootstrapCfg, client.Options{Scheme: scheme})
	if err != nil {
		return Result{}, fmt.Errorf("creating bootstrap client for TLS profile: %w", err)
	}

	return resolveClusterProfile(resolveCtx, k8sClient)
}

func resolveClusterProfile(ctx context.Context, k8sClient client.Reader) (Result, error) {
	apiServer := &configv1.APIServer{}
	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2,
		Steps:    3,
	}
	var permanentErr error
	var fetched bool
	retryErr := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: apiServerName}, apiServer); err != nil {
			switch {
			case meta.IsNoMatchError(err):
				log.Info("TLS profile not available, using hardened defaults (non-OpenShift cluster)")
				return true, nil
			case apierrors.IsNotFound(err):
				log.Info("APIServer resource not found, using hardened defaults")
				return true, nil
			case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
				hint := "ensure ClusterRole grants get/list/watch on config.openshift.io/apiservers"
				log.Info("APIServer TLS profile authorization failed",
					"hint", hint,
					"error", err)
				permanentErr = fmt.Errorf("reading APIServer TLS profile: %w; %s", err, hint)
				return false, permanentErr
			case apierrors.IsServiceUnavailable(err),
				apierrors.IsTimeout(err),
				apierrors.IsServerTimeout(err),
				apierrors.IsTooManyRequests(err),
				errors.Is(err, context.DeadlineExceeded):
				log.Info("Transient API error reading TLS profile, retrying", "error", err)
				return false, nil
			default:
				permanentErr = fmt.Errorf("reading APIServer TLS profile: %w", err)
				return false, permanentErr
			}
		}
		fetched = true
		return true, nil
	})
	if permanentErr != nil {
		return Result{}, permanentErr
	}
	if retryErr != nil {
		log.Info("Failed to fetch TLS profile after retries, using Intermediate fallback", "error", retryErr)
		return intermediateResult(false, true), nil
	}
	if !fetched {
		return intermediateResult(false, false), nil
	}

	settings := settingsFromAPIServer(apiServer)
	profile := apiServer.Spec.TLSSecurityProfile
	if !shouldHonorClusterTLSProfile(apiServer.Spec.TLSAdherence) {
		profile = &configv1.TLSSecurityProfile{Type: configv1.TLSProfileIntermediateType}
	}
	if profile == nil {
		profile = &configv1.TLSSecurityProfile{Type: configv1.TLSProfileIntermediateType}
	}
	switch profile.Type {
	case "", configv1.TLSProfileIntermediateType, configv1.TLSProfileModernType, configv1.TLSProfileOldType:
	case configv1.TLSProfileCustomType:
		if profile.Custom == nil {
			return Result{}, fmt.Errorf("TLS profile type %q has no Custom settings", profile.Type)
		}
		if _, ok := ocpTLSVersionMap[profile.Custom.MinTLSVersion]; !ok {
			return Result{}, fmt.Errorf("TLS profile type %q has unsupported minTLSVersion %q", profile.Type, profile.Custom.MinTLSVersion)
		}
	default:
		log.Info("Unknown TLS profile type, using Intermediate fallback", "type", profile.Type)
		profile = &configv1.TLSSecurityProfile{Type: configv1.TLSProfileIntermediateType}
	}

	minVersion, ciphers, err := parseProfile(profile)
	if err != nil {
		return Result{}, err
	}
	minVersion, ciphers = finalizeClusterTLSOpts(minVersion, ciphers)

	log.Info("Resolved TLS configuration from cluster security profile",
		"adherence", settings.Adherence,
		"type", profile.Type,
		"minVersion", minVersion)

	return Result{
		TLSOpts:         tlsOptsFrom(minVersion, ciphers),
		ProfileFetched:  true,
		APIAvailable:    true,
		InitialSettings: settings,
	}, nil
}

func finalizeClusterTLSOpts(minVersion uint16, ciphers []uint16) (uint16, []uint16) {
	if minVersion >= tls.VersionTLS13 && len(ciphers) > 0 {
		log.Info("Ignoring cipher suites for TLS 1.3 cluster profile; Go manages TLS 1.3 ciphers internally")
		return minVersion, nil
	}
	return minVersion, ciphers
}

func intermediateResult(fetched, apiAvailable bool) Result {
	return Result{
		TLSOpts:        tlsOptsFrom(tls.VersionTLS12, intermediateCiphers),
		ProfileFetched: fetched,
		APIAvailable:   apiAvailable,
		InitialSettings: Settings{
			ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
	}
}

func resolveProfileSpec(profile *configv1.TLSSecurityProfile) *configv1.TLSProfileSpec {
	if profile == nil {
		return configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	}
	switch profile.Type {
	case configv1.TLSProfileModernType:
		return configv1.TLSProfiles[configv1.TLSProfileModernType]
	case configv1.TLSProfileOldType:
		return configv1.TLSProfiles[configv1.TLSProfileOldType]
	case configv1.TLSProfileCustomType:
		if profile.Custom != nil {
			return &profile.Custom.TLSProfileSpec
		}
		return configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	default:
		return configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	}
}

// parseProfile resolves the honored profile to Go TLS values. Named profiles come
// from configv1.TLSProfiles; Custom from its spec. Malformed values error instead
// of silently substituting Intermediate.
func parseProfile(profile *configv1.TLSSecurityProfile) (uint16, []uint16, error) {
	if profile == nil {
		return tls.VersionTLS12, intermediateCiphers, nil
	}

	switch profile.Type {
	case configv1.TLSProfileIntermediateType, "":
		return tls.VersionTLS12, intermediateCiphers, nil
	case configv1.TLSProfileModernType:
		return tls.VersionTLS13, nil, nil
	case configv1.TLSProfileOldType:
		return parseSpecCiphers(configv1.TLSProfiles[configv1.TLSProfileOldType])
	case configv1.TLSProfileCustomType:
		if profile.Custom == nil {
			return 0, nil, fmt.Errorf("TLS profile type %q has no Custom settings", profile.Type)
		}
		return parseSpecCiphers(&profile.Custom.TLSProfileSpec)
	default:
		log.Info("Unknown TLS profile type, falling back to Intermediate", "type", profile.Type)
		return tls.VersionTLS12, intermediateCiphers, nil
	}
}

// parseSpecCiphers maps a profile spec's minimum version and cipher names to Go
// values, pruning cipher names the runtime cannot negotiate. The configured
// minimum is honored exactly: Go 1.22+ defaults the server minimum to TLS 1.2
// only when tls.Config.MinVersion is unset; the explicit minimum needs no GODEBUG.
func parseSpecCiphers(spec *configv1.TLSProfileSpec) (uint16, []uint16, error) {
	minVersion, ok := ocpTLSVersionMap[spec.MinTLSVersion]
	if !ok {
		return 0, nil, fmt.Errorf("unsupported minTLSVersion %q", spec.MinTLSVersion)
	}

	var ciphers []uint16
	for _, name := range spec.Ciphers {
		if id, ok := goCipherID(name, minVersion); ok {
			ciphers = append(ciphers, id)
		} else {
			log.Info("Dropping cipher not negotiable by Go", "cipher", name, "minTLSVersion", spec.MinTLSVersion)
		}
	}
	if len(spec.Ciphers) > 0 && len(ciphers) == 0 {
		return 0, nil, fmt.Errorf("no usable ciphers at min version %s (ciphers: %v)", spec.MinTLSVersion, spec.Ciphers)
	}
	return minVersion, ciphers, nil
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
