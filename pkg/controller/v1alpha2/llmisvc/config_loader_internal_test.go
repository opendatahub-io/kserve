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

package llmisvc

import (
	"crypto/tls"
	"regexp"
	"strings"
	"testing"

	kservetls "github.com/kserve/kserve/pkg/tls"
)

// The vLLM presets interpolate the converted cipher list into a shell command, so
// every value this map can produce has to survive that unquoted. Guarding the map
// here catches an unsafe entry when it is written, rather than at config load in
// somebody's cluster.
func TestOpenSSLCipherSuiteNamesAreShellSafe(t *testing.T) {
	// The value the presets interpolate is the joined list, so assert on what
	// openSSLCipherSuites returns rather than on the map values alone - the ":"
	// separator is part of what reaches the shell.
	all := make([]string, 0, len(openSSLCipherSuiteNames))
	for goName := range openSSLCipherSuiteNames {
		all = append(all, goName)
	}
	joined, err := openSSLCipherSuites(strings.Join(all, ","))
	if err != nil {
		t.Fatalf("converting every mapped suite failed: %v", err)
	}
	if shellSafe := regexp.MustCompile(`^[A-Za-z0-9:_-]+$`); !shellSafe.MatchString(joined) {
		t.Errorf("the converted list %q is not safe to interpolate into a shell command", joined)
	}
}

// A name Go accepts for TLS 1.2 but the map cannot express fails config loading
// for every LLMInferenceService, so the two sets must not drift apart.
func TestOpenSSLCipherSuiteNamesCoverEveryConfigurableSuite(t *testing.T) {
	suites := append(tls.CipherSuites(), tls.InsecureCipherSuites()...)
	for _, suite := range suites {
		// Membership comes from the validator the config path runs, not from a
		// second reading of crypto/tls: if Validate widens, this must follow.
		if err := kservetls.Validate("VersionTLS12", suite.Name); err != nil {
			continue
		}
		if _, ok := openSSLCipherSuiteNames[suite.Name]; !ok {
			t.Errorf("%s is configurable for TLS 1.2 but has no OpenSSL name, so configuring it fails config loading", suite.Name)
		}
	}
}
