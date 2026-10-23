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
	"crypto/fips140"
	"crypto/tls"
	"errors"
	"fmt"
	"strconv"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResolveWorkloadArgs reads the cluster profile for a consumer workload. A
// missing APIServer object (including non-OpenShift clusters) uses the
// hardened Intermediate defaults.
func ResolveWorkloadArgs(ctx context.Context, reader client.Reader) (WorkloadTLSArgs, error) {
	args, _, err := resolveWorkloadArgs(ctx, reader)
	return args, err
}

// resolveWorkloadArgs also reports whether the OpenShift APIServer object was
// readable. Consumers that apply an OpenShift profile directly can leave their
// workloads untouched when the resource is unavailable, while Go proxies keep
// using the hardened Intermediate fallback through ResolveWorkloadArgs.
func resolveWorkloadArgs(ctx context.Context, reader client.Reader) (WorkloadTLSArgs, bool, error) {
	if reader == nil {
		args, err := workloadArgsFromSettings(Settings{ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileIntermediateType]}, true)
		return args, false, err
	}
	apiServer := &configv1.APIServer{}
	if err := reader.Get(ctx, client.ObjectKey{Name: apiServerName}, apiServer); err != nil {
		if apierrors.IsNotFound(err) || apiMeta.IsNoMatchError(err) {
			args, fallbackErr := workloadArgsFromSettings(Settings{ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileIntermediateType]}, true)
			return args, false, fallbackErr
		}
		return WorkloadTLSArgs{}, false, fmt.Errorf("reading APIServer TLS profile for workload: %w", err)
	}
	settings, err := workloadSettingsFromAPIServer(apiServer)
	if err != nil {
		return WorkloadTLSArgs{}, true, err
	}
	// The sidecar is a separate strict-FIPS binary; its curve policy must not
	// depend on whether the manager process itself was built in FIPS mode.
	args, err := workloadArgsFromSettings(settings, true)
	return args, true, err
}

var goCurveIDs = map[string]uint16{
	string(configv1.TLSGroupSecP256r1):          uint16(tls.CurveP256),
	string(configv1.TLSGroupSecP384r1):          uint16(tls.CurveP384),
	string(configv1.TLSGroupSecP521r1):          uint16(tls.CurveP521),
	string(configv1.TLSGroupX25519):             uint16(tls.X25519),
	string(configv1.TLSGroupX25519MLKEM768):     uint16(tls.X25519MLKEM768),
	string(configv1.TLSGroupSecP256r1MLKEM768):  uint16(tls.SecP256r1MLKEM768),
	string(configv1.TLSGroupSecP384r1MLKEM1024): uint16(tls.SecP384r1MLKEM1024),
}

var fipsApprovedCurveIDs = map[uint16]struct{}{
	uint16(tls.CurveP256):          {},
	uint16(tls.CurveP384):          {},
	uint16(tls.CurveP521):          {},
	uint16(tls.SecP256r1MLKEM768):  {},
	uint16(tls.SecP384r1MLKEM1024): {},
}

// CurveIDsFromNames converts OpenShift curve names to the numeric IDs used by
// Go-based proxy flags, dropping names that are not supported by the consumer.
func CurveIDsFromNames(names []configv1.TLSGroup, fipsMode bool) string {
	ids := make([]string, 0, len(names))
	for _, name := range names {
		id, ok := goCurveIDs[string(name)]
		if ok && (!fipsMode || curveIDFIPSApproved(id)) {
			ids = append(ids, strconv.FormatUint(uint64(id), 10))
		}
	}
	return strings.Join(ids, ",")
}

func curveIDFIPSApproved(id uint16) bool {
	_, ok := fipsApprovedCurveIDs[id]
	return ok
}

func workloadSettingsFromAPIServer(apiServer *configv1.APIServer) (Settings, error) {
	if !shouldHonorClusterTLSProfile(apiServer.Spec.TLSAdherence) {
		return Settings{
			ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
			Adherence:   apiServer.Spec.TLSAdherence,
		}, nil
	}

	profile := apiServer.Spec.TLSSecurityProfile
	if profile == nil {
		return Settings{
			ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
			Adherence:   apiServer.Spec.TLSAdherence,
		}, nil
	}

	var profileSpec *configv1.TLSProfileSpec
	switch profile.Type {
	case configv1.TLSProfileOldType, configv1.TLSProfileIntermediateType, configv1.TLSProfileModernType:
		profileSpec = resolveProfileSpec(profile)
	case configv1.TLSProfileCustomType:
		if profile.Custom == nil {
			return Settings{}, errors.New("strict Custom TLS profile is missing its custom settings")
		}
		if profile.Custom.MinTLSVersion == "" {
			return Settings{}, errors.New("strict Custom TLS profile is missing minTLSVersion")
		}
		profileSpec = &profile.Custom.TLSProfileSpec
	default:
		return Settings{}, fmt.Errorf("unsupported strict TLS profile type %q", profile.Type)
	}

	return Settings{ProfileSpec: *profileSpec, Adherence: apiServer.Spec.TLSAdherence}, nil
}

// WorkloadTLSArgs contains the resolved TLS arguments for workload consumers.
// CipherSuites are Go names for TLS proxies; OpenSSLCipherSuites are canonical
// OpenSSL names for Python's ssl module. Cipher suites are omitted for TLS 1.3
// because consumers manage TLS 1.3 cipher selection internally.
type WorkloadTLSArgs struct {
	MinVersion          string
	CipherSuites        string
	OpenSSLCipherSuites string
	CurveIDs            string
}

// WorkloadArgsFromSettings converts the effective OpenShift TLS settings into
// arguments for a consumer-side Go TLS proxy. The proxy does not resolve or
// watch TLSSecurityProfile itself; its owner supplies these resolved values.
func WorkloadArgsFromSettings(settings Settings) (WorkloadTLSArgs, error) {
	return workloadArgsFromSettings(settings, fips140.Enabled())
}

func workloadArgsFromSettings(settings Settings, fipsMode bool) (WorkloadTLSArgs, error) {
	minVersion := settings.ProfileSpec.MinTLSVersion
	if minVersion == "" {
		minVersion = "VersionTLS12"
	}
	version, ok := ocpTLSVersionMap[minVersion]
	if !ok {
		return WorkloadTLSArgs{}, fmt.Errorf("unsupported TLS minimum version %q", minVersion)
	}

	args := WorkloadTLSArgs{MinVersion: string(minVersion)}
	if len(settings.ProfileSpec.Groups) > 0 {
		args.CurveIDs = CurveIDsFromNames(settings.ProfileSpec.Groups, fipsMode)
		if args.CurveIDs == "" {
			return WorkloadTLSArgs{}, fmt.Errorf("TLS profile specifies groups but none are supported by this workload (groups: %v)", settings.ProfileSpec.Groups)
		}
	}
	if version >= tls.VersionTLS13 {
		return args, nil
	}

	ciphers := make([]string, 0, len(settings.ProfileSpec.Ciphers))
	openSSLCiphers := make([]string, 0, len(settings.ProfileSpec.Ciphers))
	for _, cipher := range settings.ProfileSpec.Ciphers {
		id, supported := openSSLToGoCipher[cipher]
		if !supported {
			continue
		}
		// crypto/tls does not permit configuring TLS 1.3 cipher suites. OpenShift
		// profiles include them alongside TLS 1.2 suites, so omit them here.
		if id == tls.TLS_AES_128_GCM_SHA256 || id == tls.TLS_AES_256_GCM_SHA384 || id == tls.TLS_CHACHA20_POLY1305_SHA256 {
			continue
		}
		if name := goCipherSuiteName(id); name != "" {
			openSSLCipher := openSSLCipherSuiteName(id)
			if openSSLCipher == "" {
				return WorkloadTLSArgs{}, fmt.Errorf("TLS profile cipher %q has no OpenSSL equivalent", cipher)
			}
			ciphers = append(ciphers, name)
			openSSLCiphers = append(openSSLCiphers, openSSLCipher)
		}
	}
	if len(settings.ProfileSpec.Ciphers) > 0 && len(ciphers) == 0 {
		return WorkloadTLSArgs{}, errors.New("TLS profile specifies no cipher suites supported by Go")
	}
	args.CipherSuites = strings.Join(ciphers, ",")
	args.OpenSSLCipherSuites = strings.Join(openSSLCiphers, ",")
	return args, nil
}

// goCipherSuiteName returns the crypto/tls constant name accepted by
// kube-rbac-proxy's --tls-cipher-suites flag. OpenShift stores cipher suites
// using OpenSSL names, which are not accepted by that flag.
func goCipherSuiteName(id uint16) string {
	for _, suite := range tls.CipherSuites() {
		if suite.ID == id {
			return suite.Name
		}
	}
	for _, suite := range tls.InsecureCipherSuites() {
		if suite.ID == id {
			return suite.Name
		}
	}
	return ""
}

// openSSLCipherSuiteName returns a canonical OpenSSL cipher name for Python's
// ssl module based on the resolved Go cipher ID, avoiding profile aliases that
// OpenSSL does not accept.
func openSSLCipherSuiteName(id uint16) string {
	switch id {
	case tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256:
		return "ECDHE-ECDSA-AES128-GCM-SHA256"
	case tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256:
		return "ECDHE-RSA-AES128-GCM-SHA256"
	case tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384:
		return "ECDHE-ECDSA-AES256-GCM-SHA384"
	case tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384:
		return "ECDHE-RSA-AES256-GCM-SHA384"
	case tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256:
		return "ECDHE-ECDSA-CHACHA20-POLY1305"
	case tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256:
		return "ECDHE-RSA-CHACHA20-POLY1305"
	default:
		return ""
	}
}
