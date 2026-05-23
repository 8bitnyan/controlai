package pki

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"time"
)

// LeafCert holds a newly issued leaf certificate and its (un-stored) private key.
type LeafCert struct {
	CertPEM     []byte // PEM-encoded leaf cert
	KeyPEM      []byte // PEM-encoded private key — returned once, never persisted server-side
	Fingerprint string // SHA-256 hex of DER cert
	NotBefore   time.Time
	NotAfter    time.Time
}

// IssueLeafCert issues a ClientAuth leaf certificate signed by ca.
// cn should be the slugified gateway name (≤63 chars).
// ttlDays sets the validity period (default 365 if ≤0).
func IssueLeafCert(ca *CA, cn string, ttlDays int) (*LeafCert, error) {
	if err := ca.ParseCert(); err != nil {
		return nil, err
	}
	if ca.key == nil {
		return nil, fmt.Errorf("CA key not decrypted: call DecryptKey first")
	}
	if ttlDays <= 0 {
		ttlDays = 365
	}
	if len(cn) > 63 {
		cn = cn[:63]
	}

	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}

	now := time.Now().Add(-5 * time.Minute) // clock-skew tolerance
	tmpl := &x509.Certificate{
		SerialNumber: mustSerial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now,
		NotAfter:     now.AddDate(0, 0, ttlDays),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("sign leaf cert: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal leaf key: %w", err)
	}

	return &LeafCert{
		CertPEM:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		KeyPEM:      pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		Fingerprint: fingerprint(certDER),
		NotBefore:   tmpl.NotBefore,
		NotAfter:    tmpl.NotAfter,
	}, nil
}
