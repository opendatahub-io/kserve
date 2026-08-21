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

package llmisvc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// generateCACert creates a self-signed CA certificate using the provided key.
func generateCACert(t *testing.T, key crypto.Signer) []byte {
	t.Helper()

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"Test CA"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, key.Public(), key)
	if err != nil {
		t.Fatalf("failed to create CA certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
}

func TestLoadCAFromSecret_PKCS8RSA(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	keyBytes, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatalf("failed to marshal PKCS8 key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
	certPEM := generateCACert(t, rsaKey)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ca", Namespace: "test-ns"},
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	caCert, signer, caCertPEM, err := loadCAFromSecret(context.Background(), c, "test-ca", "test-ns")
	if err != nil {
		t.Fatalf("loadCAFromSecret failed: %v", err)
	}
	if caCert == nil {
		t.Fatal("expected non-nil CA certificate")
	}
	if signer == nil {
		t.Fatal("expected non-nil signer")
	}
	if _, ok := signer.(*rsa.PrivateKey); !ok {
		t.Fatalf("expected *rsa.PrivateKey signer, got %T", signer)
	}
	if len(caCertPEM) == 0 {
		t.Fatal("expected non-empty CA cert PEM")
	}
}

func TestLoadCAFromSecret_PKCS1RSA(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	keyBytes := x509.MarshalPKCS1PrivateKey(rsaKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes})
	certPEM := generateCACert(t, rsaKey)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ca", Namespace: "test-ns"},
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	caCert, signer, _, err := loadCAFromSecret(context.Background(), c, "test-ca", "test-ns")
	if err != nil {
		t.Fatalf("loadCAFromSecret failed: %v", err)
	}
	if caCert == nil {
		t.Fatal("expected non-nil CA certificate")
	}
	if _, ok := signer.(*rsa.PrivateKey); !ok {
		t.Fatalf("expected *rsa.PrivateKey signer, got %T", signer)
	}
}

func TestLoadCAFromSecret_EC_SEC1(t *testing.T) {
	t.Parallel()

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate EC key: %v", err)
	}

	// Marshal as SEC 1 / EC format (what OCP 4.22 uses).
	keyBytes, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatalf("failed to marshal EC key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	certPEM := generateCACert(t, ecKey)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ca", Namespace: "test-ns"},
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	caCert, signer, _, err := loadCAFromSecret(context.Background(), c, "test-ca", "test-ns")
	if err != nil {
		t.Fatalf("loadCAFromSecret failed: %v", err)
	}
	if caCert == nil {
		t.Fatal("expected non-nil CA certificate")
	}
	if _, ok := signer.(*ecdsa.PrivateKey); !ok {
		t.Fatalf("expected *ecdsa.PrivateKey signer, got %T", signer)
	}
}
