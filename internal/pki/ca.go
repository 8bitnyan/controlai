// Package pki implements per-site PKI for controlai: CA generation,
// leaf-cert issuance, server-cert issuance, AES-256-GCM key wrapping,
// and automatic rotation trigger detection.
package pki

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"
)

const (
	rsaBits        = 2048
	caTTLYears     = 20
	serverTTLYears = 10
)

// MasterKeyEnvVar is the environment variable name for the AES-256-GCM master key.
const MasterKeyEnvVar = "CONTROLAI_CA_KEY_ENCRYPTION_KEY"

// MasterKey reads and validates the 32-byte hex-encoded master key from env.
// Returns ErrNoMasterKey if the env var is unset in non-dev mode.
func MasterKey(devMode bool) ([]byte, error) {
	val := os.Getenv(MasterKeyEnvVar)
	if val == "" {
		if devMode {
			// Use a fixed insecure key in dev mode only.
			return make([]byte, 32), nil
		}
		return nil, ErrNoMasterKey
	}
	key, err := hex.DecodeString(val)
	if err != nil {
		return nil, fmt.Errorf("parse %s: expected 64 hex chars: %w", MasterKeyEnvVar, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes (64 hex chars), got %d bytes", MasterKeyEnvVar, len(key))
	}
	return key, nil
}

// ErrNoMasterKey is returned when CONTROLAI_CA_KEY_ENCRYPTION_KEY is unset in production mode.
var ErrNoMasterKey = errors.New("CONTROLAI_CA_KEY_ENCRYPTION_KEY is not set; daemon cannot start in production mode")

// CA holds a generated certificate authority.
type CA struct {
	// CertPEM is the PEM-encoded CA certificate.
	CertPEM []byte
	// KeyEncrypted is the AES-256-GCM ciphertext of the PKCS8-encoded private key.
	KeyEncrypted []byte
	// KeyNonce is the 12-byte GCM nonce used during encryption.
	KeyNonce []byte
	// Fingerprint is the SHA-256 hex of the DER-encoded certificate.
	Fingerprint string
	// cert is the parsed cert, kept in memory for issuance.
	cert *x509.Certificate
	// key is the decrypted private key, kept in memory for issuance.
	key *rsa.PrivateKey
}

// GenerateCA creates a new RSA-2048 self-signed CA for the given tenantID and siteID.
// The private key is immediately wrapped with AES-256-GCM using masterKey.
func GenerateCA(tenantID, siteID string, masterKey []byte) (*CA, error) {
	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}

	cn := fmt.Sprintf("controlai-%s-%s-ca", tenantID, siteID)
	now := time.Now().Add(-5 * time.Minute) // clock-skew tolerance
	tmpl := &x509.Certificate{
		SerialNumber: mustSerial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now,
		NotAfter:     now.AddDate(caTTLYears, 0, 0),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create CA cert: %w", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	fp := fingerprint(certDER)

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal CA key: %w", err)
	}
	encrypted, nonce, err := aesGCMEncrypt(masterKey, keyDER)
	if err != nil {
		return nil, fmt.Errorf("encrypt CA key: %w", err)
	}

	return &CA{
		CertPEM:      certPEM,
		KeyEncrypted: encrypted,
		KeyNonce:     nonce,
		Fingerprint:  fp,
		cert:         cert,
		key:          key,
	}, nil
}

// DecryptKey decrypts the CA private key using masterKey.
func (ca *CA) DecryptKey(masterKey []byte) error {
	if ca.key != nil {
		return nil
	}
	keyDER, err := aesGCMDecrypt(masterKey, ca.KeyEncrypted, ca.KeyNonce)
	if err != nil {
		return fmt.Errorf("decrypt CA key: %w", err)
	}
	k, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return fmt.Errorf("parse CA key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return fmt.Errorf("CA key is not RSA")
	}
	ca.key = rk
	return nil
}

// ParseCert parses the PEM cert into ca.cert for issuance.
func (ca *CA) ParseCert() error {
	if ca.cert != nil {
		return nil
	}
	block, _ := pem.Decode(ca.CertPEM)
	if block == nil {
		return fmt.Errorf("parse CA cert PEM: empty block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
	}
	ca.cert = cert
	return nil
}

// ─── AES-256-GCM helpers ─────────────────────────────────────────────────────

func aesGCMEncrypt(key, plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	return ct, nonce, nil
}

func aesGCMDecrypt(key, ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// ─── Utilities ───────────────────────────────────────────────────────────────

func mustSerial() *big.Int {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return new(big.Int).SetBytes(b)
}

func fingerprint(certDER []byte) string {
	h := sha256.Sum256(certDER)
	return hex.EncodeToString(h[:])
}
