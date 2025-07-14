package llmisvc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmeta"
)

const (
	certificateDuration                      = time.Hour * 24 * 365 * 10 // 10 years
	certificateExpirationRenewBufferDuration = time.Hour * 24 * 30       // 30 days

	certificatesExpirationAnnotation = "certificates.kserve.io/expiration"
)

func (r *LLMInferenceServiceReconciler) reconcileSelfSignedCertsSecret(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	expected, err := r.expectedSelfSignedCertsSecret(llmSvc)
	if err != nil {
		return fmt.Errorf("failed to get expected self-signed certificate secret: %v", err)
	}
	if err := Reconcile(ctx, r, llmSvc, &corev1.Secret{}, expected, semanticCertificateSecretIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile self-signed TLS certificate: %w", err)
	}
	return nil
}

func (r *LLMInferenceServiceReconciler) expectedSelfSignedCertsSecret(llmSvc *v1alpha1.LLMInferenceService) (*corev1.Secret, error) {
	keyBytes, certBytes, err := createSelfSignedTLSCertificate()
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
		},
		Data: map[string][]byte{
			"tls.crt": certBytes,
			"tls.key": keyBytes,
		},
		Type: corev1.SecretTypeTLS,
	}
	return expected, nil
}

// createSelfSignedTLSCertificate creates a self-signed cert the server can use to serve TLS.
func createSelfSignedTLSCertificate() ([]byte, []byte, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating serial number: %v", err)
	}
	now := time.Now()
	notBefore := now.UTC()
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Kserve Self Signed"},
		},
		NotBefore:             notBefore,
		NotAfter:              now.Add(certificateDuration).UTC(),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	priv, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, fmt.Errorf("error generating key: %v", err)
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create TLS certificate: %v", err)
	}
	certBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshall TLS private key: %v", err)
	}
	keyBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})

	return keyBytes, certBytes, nil
}

// semanticCertificateSecretIsEqual is a semantic comparison for secrets that is specifically meant to compare TLS
// certificates secrets handling expiration and renewal.
func semanticCertificateSecretIsEqual(expected *corev1.Secret, curr *corev1.Secret) bool {
	expires, ok := curr.Annotations[certificatesExpirationAnnotation]
	if ok {
		t, err := time.Parse(time.RFC3339, expires)
		if err == nil && time.Now().UTC().After(t.UTC()) {
			return equality.Semantic.DeepDerivative(expected.Data, curr.Data) &&
				equality.Semantic.DeepDerivative(expected.StringData, curr.StringData) &&
				equality.Semantic.DeepDerivative(expected.Immutable, curr.Immutable) &&
				equality.Semantic.DeepDerivative(expected.Labels, curr.Labels) &&
				equality.Semantic.DeepDerivative(expected.Annotations, curr.Annotations) &&
				equality.Semantic.DeepDerivative(expected.Type, curr.Type)
		}
	}

	return equality.Semantic.DeepDerivative(expected.Immutable, curr.Immutable) &&
		equality.Semantic.DeepDerivative(expected.Labels, curr.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, curr.Annotations) &&
		equality.Semantic.DeepDerivative(expected.Type, curr.Type)
}
