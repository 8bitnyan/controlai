package pki

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"net"
	"time"
)

// ServerCert holds a newly issued server certificate and its private key.
type ServerCert struct {
	CertPEM     []byte
	KeyPEM      []byte
	Fingerprint string
	NotBefore   time.Time
	NotAfter    time.Time
	SANs        []string // DNS SANs that were embedded
}

// IssueServerCert issues a ServerAuth+ClientAuth server certificate signed by ca.
// sans contains the primary DNS name plus any operator-provided aliases.
func IssueServerCert(ca *CA, sans []string) (*ServerCert, error) {
	if err := ca.ParseCert(); err != nil {
		return nil, err
	}
	if ca.key == nil {
		return nil, fmt.Errorf("CA key not decrypted: call DecryptKey first")
	}
	if len(sans) == 0 {
		return nil, fmt.Errorf("server cert requires at least one SAN")
	}

	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return nil, fmt.Errorf("generate server key: %w", err)
	}

	now := time.Now().Add(-5 * time.Minute)
	tmpl := &x509.Certificate{
		SerialNumber: mustSerial(),
		Subject:      pkix.Name{CommonName: sans[0]},
		NotBefore:    now,
		NotAfter:     now.AddDate(serverTTLYears, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     sans,
		IPAddresses:  []net.IP{}, // no IP SANs in MVP
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("sign server cert: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal server key: %w", err)
	}

	return &ServerCert{
		CertPEM:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		KeyPEM:      pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		Fingerprint: fingerprint(certDER),
		NotBefore:   tmpl.NotBefore,
		NotAfter:    tmpl.NotAfter,
		SANs:        sans,
	}, nil
}
