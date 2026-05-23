package pki_test

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"controlai/internal/pki"
)

var testMasterKey = make([]byte, 32) // all zeros — dev mode

func TestGenerateCAAndDecryptKey(t *testing.T) {
	ca, err := pki.GenerateCA("tnt_acme-corp", "ste_seoul", testMasterKey)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if ca.CertPEM == nil {
		t.Error("expected non-nil CertPEM")
	}
	if ca.KeyEncrypted == nil || ca.KeyNonce == nil {
		t.Error("expected encrypted key and nonce")
	}
	if ca.Fingerprint == "" {
		t.Error("expected non-empty fingerprint")
	}

	// Decrypt and parse.
	if err := ca.DecryptKey(testMasterKey); err != nil {
		t.Fatalf("DecryptKey: %v", err)
	}
	if err := ca.ParseCert(); err != nil {
		t.Fatalf("ParseCert: %v", err)
	}
}

func TestIssueLeafCert(t *testing.T) {
	ca, _ := pki.GenerateCA("tnt_t", "ste_s", testMasterKey)
	_ = ca.DecryptKey(testMasterKey)
	_ = ca.ParseCert()

	leaf, err := pki.IssueLeafCert(ca, "floor-1-pump", 365)
	if err != nil {
		t.Fatalf("IssueLeafCert: %v", err)
	}
	if len(leaf.CertPEM) == 0 || len(leaf.KeyPEM) == 0 {
		t.Error("expected non-empty CertPEM and KeyPEM")
	}

	// Verify leaf against CA.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)
	block, _ := pem.Decode(leaf.CertPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		t.Errorf("leaf cert verification failed: %v", err)
	}
	// CN should match.
	if cert.Subject.CommonName != "floor-1-pump" {
		t.Errorf("expected CN floor-1-pump, got %s", cert.Subject.CommonName)
	}
}

func TestIssueServerCert(t *testing.T) {
	ca, _ := pki.GenerateCA("tnt_t", "ste_s", testMasterKey)
	_ = ca.DecryptKey(testMasterKey)
	_ = ca.ParseCert()

	sans := []string{"ste_s.tnt_t.example.com", "ste_s.tnt_t.controlai.local"}
	srv, err := pki.IssueServerCert(ca, sans)
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)
	block, _ := pem.Decode(srv.CertPEM)
	cert, _ := x509.ParseCertificate(block.Bytes)
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   sans[0],
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		t.Errorf("server cert verification failed: %v", err)
	}
}

func TestNeedsRotation_Missing(t *testing.T) {
	reason, err := pki.NeedsRotation("/nonexistent/cert.pem", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != pki.ReasonMissing {
		t.Errorf("expected ReasonMissing, got %q", reason)
	}
}

func TestNeedsRotation_NotExpiring(t *testing.T) {
	// Issue a fresh cert and write to temp file.
	ca, _ := pki.GenerateCA("tnt_t", "ste_s", testMasterKey)
	_ = ca.DecryptKey(testMasterKey)
	_ = ca.ParseCert()
	leaf, _ := pki.IssueLeafCert(ca, "gw", 365)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	_ = os.WriteFile(certPath, leaf.CertPEM, 0o600)

	reason, err := pki.NeedsRotation(certPath, ca.CertPEM, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != "" {
		t.Errorf("expected no rotation needed for fresh cert, got reason %q", reason)
	}
}

func TestAESGCMRoundTrip(t *testing.T) {
	// Indirect test via CA generate + decrypt.
	ca, err := pki.GenerateCA("tnt_t", "ste_s", testMasterKey)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	// DecryptKey should succeed.
	if err := ca.DecryptKey(testMasterKey); err != nil {
		t.Errorf("DecryptKey with correct key failed: %v", err)
	}
	// Wrong key should fail.
	wrongKey := make([]byte, 32)
	wrongKey[0] = 0xFF
	ca2, _ := pki.GenerateCA("tnt_t", "ste_s", testMasterKey) // fresh CA for the wrong-key test
	if err := ca2.DecryptKey(wrongKey); err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

func TestCAValidityPeriod(t *testing.T) {
	ca, _ := pki.GenerateCA("tnt_t", "ste_s", testMasterKey)
	_ = ca.ParseCert()
	block, _ := pem.Decode(ca.CertPEM)
	cert, _ := x509.ParseCertificate(block.Bytes)
	// CA should be valid for 20 years.
	expectedExpiry := time.Now().AddDate(20, 0, 0)
	diff := cert.NotAfter.Sub(expectedExpiry)
	if diff < -24*time.Hour || diff > 24*time.Hour {
		t.Errorf("CA expiry unexpected: got %v, want ~%v", cert.NotAfter, expectedExpiry)
	}
}
