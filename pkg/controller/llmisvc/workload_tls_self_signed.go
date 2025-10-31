/*
Copyright 2025 The KServe Authors.

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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"knative.dev/pkg/kmeta"
	"knative.dev/pkg/network"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

const (
	certificateDuration                      = time.Hour * 24 * 365 * 10 // 10 years
	certificateExpirationRenewBufferDuration = certificateDuration / 5

	certificatesExpirationAnnotation = "certificates.kserve.io/expiration-v2"
)

var (
	ServiceCASigningSecretName      = constants.GetEnvOrDefault("SERVICE_CA_SIGNING_SECRET_NAME", "signing-key")
	ServiceCASigningSecretNamespace = constants.GetEnvOrDefault("SERVICE_CA_SIGNING_SECRET_NAMESPACE", "openshift-service-ca")
)

func (r *LLMInferenceServiceReconciler) reconcileSelfSignedCertsSecret(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	log.FromContext(ctx).Info("Reconciling self-signed certificates secret")

	ips, err := r.collectIPAddresses(ctx, llmSvc)
	if err != nil {
		return fmt.Errorf("failed to collect IP addresses: %w", err)
	}
	dnsNames := r.collectDNSNames(ctx, llmSvc)

	// Generating a new certificate is quite slow and expensive as it generates a new certificate, check if the current
	// self-signed certificate (if any) is expired before creating a new one.
	var certFunc createCertFunc = r.createSelfSignedTLSCertificate(ctx, ips, dnsNames)
	if curr := r.getExistingSelfSignedCertificate(ctx, llmSvc); curr != nil && !ShouldRecreateCertificate(curr, dnsNames, ips) {
		certFunc = func() ([]byte, []byte, error) {
			return curr.Data["tls.key"], curr.Data["tls.crt"], nil
		}
	}

	expected, err := r.expectedSelfSignedCertsSecret(llmSvc, certFunc)
	if err != nil {
		return fmt.Errorf("failed to get expected self-signed certificate secret: %w", err)
	}
	if err := Reconcile(ctx, r, llmSvc, &corev1.Secret{}, expected, semanticCertificateSecretIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile self-signed TLS certificate: %w", err)
	}
	return nil
}

type createCertFunc func() ([]byte, []byte, error)

func (r *LLMInferenceServiceReconciler) expectedSelfSignedCertsSecret(llmSvc *v1alpha1.LLMInferenceService, certFunc createCertFunc) (*corev1.Secret, error) {
	keyBytes, certBytes, err := certFunc()
	if err != nil {
		return nil, fmt.Errorf("failed to create self-signed TLS certificate: %w", err)
	}

	expected := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-self-signed-certs"),
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				"app.kubernetes.io/component": "llminferenceservice-workload",
				"app.kubernetes.io/name":      llmSvc.GetName(),
				"app.kubernetes.io/part-of":   "llminferenceservice",
			},
			Annotations: map[string]string{
				certificatesExpirationAnnotation: time.Now().
					Add(certificateDuration - certificateExpirationRenewBufferDuration).
					Format(time.RFC3339),
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
		},
		Data: map[string][]byte{
			"tls.crt": certBytes,
			"tls.key": keyBytes,
		},
		Type: corev1.SecretTypeTLS,
	}
	return expected, nil
}

// createSelfSignedTLSCertificate creates a CA-signed certificate the server can use to serve TLS.
// It loads the CA certificate from the OpenShift service-ca secret and signs the certificate with it.
// This function requires the CA to be available and will return an error if it cannot be loaded.
// The implementation is designed to be FIPS 140-2 compliant:
// - Uses RSA 4096-bit keys (FIPS requires minimum 2048 bits)
// - Explicitly uses SHA-256 signature algorithm (FIPS-approved)
// - Uses crypto/rand for random number generation (FIPS-approved CSPRNG)
// - Encodes keys in PKCS#8 format (FIPS-compliant)
// Note: CA certificate FIPS compliance is ensured by OpenShift service-ca operator
func (r *LLMInferenceServiceReconciler) createSelfSignedTLSCertificate(ctx context.Context, ips []string, dnsNames []string) func() ([]byte, []byte, error) {
	return func() ([]byte, []byte, error) {
		// Load CA certificate from OpenShift service-ca secret (required)
		// Note: CA FIPS compliance is ensured by OpenShift service-ca operator
		caCert, caPrivKey, err := r.loadCAFromSecret(ctx, ServiceCASigningSecretName, ServiceCASigningSecretNamespace)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load CA certificate (required for signing): %w", err)
		}

		// FIPS Compliance: Use crypto/rand (FIPS-approved CSPRNG) for serial number
		serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
		serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating serial number: %w", err)
		}
		ipAddresses := make([]net.IP, 0, len(ips))
		for _, ip := range ips {
			if p := net.ParseIP(ip); p != nil {
				ipAddresses = append(ipAddresses, p)
			}
		}

		log.FromContext(ctx).Info("Creating FIPS-compliant CA-signed certificate", "ips", ips, "ipAddresses", ipAddresses)

		now := time.Now()
		notBefore := now.UTC()
		template := x509.Certificate{
			SerialNumber: serialNumber,
			Subject: pkix.Name{
				Organization: []string{"Kserve CA Signed"},
			},
			NotBefore:             notBefore,
			NotAfter:              now.Add(certificateDuration + certificateExpirationRenewBufferDuration).UTC(),
			KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true,
			DNSNames:              dnsNames,
			IPAddresses:           ipAddresses,
			// FIPS Compliance: Explicitly set signature algorithm to SHA-256 with RSA
			// This ensures we use FIPS-approved hash algorithm (SHA-256)
			SignatureAlgorithm: x509.SHA256WithRSA,
		}

		// FIPS Compliance: Generate 4096-bit RSA key (exceeds FIPS minimum of 2048 bits)
		// Using crypto/rand (FIPS-approved CSPRNG)
		priv, err := rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			return nil, nil, fmt.Errorf("error generating key: %w", err)
		}

		// Sign the certificate with the CA
		// KEY: template is the cert to create, caCert is the parent, priv.PublicKey is the new cert's public key, caPrivKey signs it
		derBytes, err := x509.CreateCertificate(rand.Reader, &template, caCert, &priv.PublicKey, caPrivKey)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create CA-signed TLS certificate: %w", err)
		}
		certBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

		// FIPS Compliance: Encode private key in PKCS#8 format (FIPS-compliant)
		privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshall TLS private key: %w", err)
		}
		keyBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})

		return keyBytes, certBytes, nil
	}
}

// loadCAFromSecret loads a CA certificate and private key from a Kubernetes secret.
// The secret is expected to have "tls.crt" and "tls.key" fields.
func (r *LLMInferenceServiceReconciler) loadCAFromSecret(ctx context.Context, secretName, secretNamespace string) (*x509.Certificate, *rsa.PrivateKey, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{
		Namespace: secretNamespace,
		Name:      secretName,
	}

	if err := r.Client.Get(ctx, key, secret); err != nil {
		return nil, nil, fmt.Errorf("failed to get CA secret %s/%s: %w", secretNamespace, secretName, err)
	}

	// Decode certificate
	certPEM, ok := secret.Data["tls.crt"]
	if !ok {
		return nil, nil, fmt.Errorf("CA secret %s/%s does not contain tls.crt", secretNamespace, secretName)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, errors.New("failed to decode certificate PEM from CA secret")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	// Decode private key
	keyPEM, ok := secret.Data["tls.key"]
	if !ok {
		return nil, nil, fmt.Errorf("CA secret %s/%s does not contain tls.key", secretNamespace, secretName)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("failed to decode private key PEM from CA secret")
	}

	// Try PKCS8 first, then PKCS1
	var caPrivKey *rsa.PrivateKey
	if key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err == nil {
		var ok bool
		caPrivKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return nil, nil, errors.New("CA private key is not an RSA key")
		}
	} else if key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes); err == nil {
		caPrivKey = key
	} else {
		return nil, nil, fmt.Errorf("failed to parse CA private key: %w", err)
	}

	return caCert, caPrivKey, nil
}

func (r *LLMInferenceServiceReconciler) getExistingSelfSignedCertificate(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *corev1.Secret {
	curr := &corev1.Secret{}
	key := client.ObjectKey{Namespace: llmSvc.GetNamespace(), Name: kmeta.ChildName(llmSvc.GetName(), "-kserve-self-signed-certs")}
	err := r.Client.Get(ctx, key, curr)
	if err != nil {
		return nil
	}
	return curr
}

func isCertificateExpired(curr *corev1.Secret) bool {
	expires, ok := curr.Annotations[certificatesExpirationAnnotation]
	if ok {
		t, err := time.Parse(time.RFC3339, expires)
		return err == nil && time.Now().UTC().After(t.UTC())
	}
	return false
}

func ShouldRecreateCertificate(curr *corev1.Secret, expectedDNSNames []string, expectedIPs []string) bool {
	if curr == nil || isCertificateExpired(curr) || len(curr.Data["tls.key"]) == 0 || len(curr.Data["tls.crt"]) == 0 {
		return true
	}

	// Decode PEM-encoded certificate
	certBlock, _ := pem.Decode(curr.Data["tls.crt"])
	if certBlock == nil {
		return true
	}
	cert, certErr := x509.ParseCertificate(certBlock.Bytes)

	// Decode PEM-encoded private key
	keyBlock, _ := pem.Decode(curr.Data["tls.key"])
	if keyBlock == nil {
		return true
	}
	_, keyErr := x509.ParsePKCS8PrivateKey(keyBlock.Bytes) // Must match createSelfSignedTLSCertificate form.

	if certErr != nil || keyErr != nil {
		return true
	}

	expectedDnsNamesSet := sets.NewString(expectedDNSNames...)
	currDnsNames := sets.NewString(cert.DNSNames...)
	if !currDnsNames.IsSuperset(expectedDnsNamesSet) {
		return true
	}

	// Only recreate certificates when the current IPs are not covering all possible IPs to account for temporary
	// changes and avoid too frequent changes [current.IsSuperset(expected)].

	expectedIpSet := sets.NewString(expectedIPs...)
	currIps := sets.NewString()
	for _, ip := range cert.IPAddresses {
		if len(ip) > 0 {
			currIps.Insert(ip.String())
		}
	}

	if !currIps.IsSuperset(expectedIpSet) {
		return true
	}

	return time.Now().UTC().After(cert.NotAfter.UTC())
}

func (r *LLMInferenceServiceReconciler) collectDNSNames(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) []string {
	dnsNames := []string{
		"localhost", // P/D sidecar sends requests for decode over localhost
		network.GetServiceHostname(kmeta.ChildName(llmSvc.GetName(), "-kserve-workload-svc"), llmSvc.GetNamespace()),
		fmt.Sprintf("%s.%s.svc", kmeta.ChildName(llmSvc.GetName(), "-kserve-workload-svc"), llmSvc.GetNamespace()),
	}

	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil && llmSvc.Spec.Router.Scheduler.Pool != nil {
		infPoolSpec := llmSvc.Spec.Router.Scheduler.Pool.Spec
		if llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
			infPool := &igwapi.InferencePool{
				ObjectMeta: metav1.ObjectMeta{Namespace: llmSvc.GetNamespace(), Name: llmSvc.Spec.Router.Scheduler.Pool.Ref.Name},
			}

			// If there is an error, this will be reported properly as part of the Router reconciliation.
			if err := r.Client.Get(ctx, client.ObjectKeyFromObject(infPool), infPool); err == nil {
				infPoolSpec = &infPool.Spec
			}
		}

		if infPoolSpec != nil {
			dnsNames = append(dnsNames, network.GetServiceHostname(string(infPoolSpec.ExtensionRef.Name), llmSvc.GetNamespace()))
			dnsNames = append(dnsNames, fmt.Sprintf("%s.%s.svc", string(infPoolSpec.ExtensionRef.Name), llmSvc.GetNamespace()))
		}
	}

	sort.Strings(dnsNames)
	return dnsNames
}

func (r *LLMInferenceServiceReconciler) collectIPAddresses(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) ([]string, error) {
	pods := &corev1.PodList{}
	listOptions := &client.ListOptions{
		Namespace: llmSvc.Namespace,
		LabelSelector: labels.SelectorFromSet(map[string]string{
			"app.kubernetes.io/name":    llmSvc.Name,
			"app.kubernetes.io/part-of": "llminferenceservice",
		}),
	}

	if err := r.Client.List(ctx, pods, listOptions); err != nil {
		return nil, fmt.Errorf("failed to list pods associated with LLM inference service: %w", err)
	}

	ips := sets.NewString("127.0.0.1") // P/D sidecar sends requests for decode over local host
	for _, pod := range pods.Items {
		ips.Insert(pod.Status.PodIP)
		for _, ip := range pod.Status.PodIPs {
			ips.Insert(ip.IP)
		}
	}

	services := &corev1.ServiceList{}
	if err := r.Client.List(ctx, services, listOptions); err != nil {
		return nil, fmt.Errorf("failed to list services associated with LLM inference service: %w", err)
	}

	for _, svc := range services.Items {
		ips.Insert(svc.Spec.ClusterIP)
		for _, ip := range svc.Spec.ClusterIPs {
			ips.Insert(ip)
		}
	}

	// List sorts IPs, so that the resulting list is always the same regardless of the order of the IPs.
	return ips.List(), nil
}

// semanticCertificateSecretIsEqual is a semantic comparison for secrets that is specifically meant to compare TLS
// certificates secrets handling expiration and renewal.
func semanticCertificateSecretIsEqual(expected *corev1.Secret, curr *corev1.Secret) bool {
	if isCertificateExpired(curr) {
		return true
	}

	expectedAnnotations := maps.Clone(expected.Annotations)
	delete(expectedAnnotations, certificatesExpirationAnnotation)

	return equality.Semantic.DeepDerivative(expected.Immutable, curr.Immutable) &&
		equality.Semantic.DeepDerivative(expected.Labels, curr.Labels) &&
		equality.Semantic.DeepDerivative(expectedAnnotations, curr.Annotations) &&
		equality.Semantic.DeepDerivative(expected.Type, curr.Type) &&
		equality.Semantic.DeepDerivative(expected.Data, curr.Data)
}
