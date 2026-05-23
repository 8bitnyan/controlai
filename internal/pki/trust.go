// Package pki – EnsureTrustFiles orchestrates cert rotation for a single site.
// It is called by the reconciler on every tick (design D9, pki-management spec).
package pki

import (
	"fmt"
	"os"
	"path/filepath"
)

// TrustFileConfig holds the parameters for EnsureTrustFiles.
type TrustFileConfig struct {
	SiteID      string
	TenantID    string
	CertDir     string // absolute path to deploy/certs/active/
	SNIHostname string // primary DNS SAN for the server cert
	MasterKey   []byte
	CACertPEM   []byte // PEM-encoded CA cert
	CAKeyEnc    []byte // AES-256-GCM encrypted CA private key
	CAKeyNonce  []byte // 12-byte GCM nonce
}

// TrustFileResult reports which cert files were rotated.
type TrustFileResult struct {
	ServerRotated   bool
	IngestorRotated bool
}

// EnsureTrustFiles checks the site's cert files for rotation triggers and
// issues replacements as needed. The following triggers are evaluated per cert:
//
//   - Missing from disk
//   - Fails verification against the site CA
//   - Expires within 30 days
//   - (Server cert only) SAN set on disk does not match SNIHostname
//
// Private keys for issued certs are written to disk under CertDir and are NOT
// returned to the caller or stored server-side beyond the file system.
func EnsureTrustFiles(cfg TrustFileConfig) (TrustFileResult, error) {
	var result TrustFileResult

	if err := os.MkdirAll(cfg.CertDir, 0o750); err != nil {
		return result, fmt.Errorf("ensure cert dir %s: %w", cfg.CertDir, err)
	}

	// Write the CA cert to disk so containers can mount it.
	caCertPath := filepath.Join(cfg.CertDir, "ca.crt")
	if _, err := os.Stat(caCertPath); err != nil {
		if err2 := os.WriteFile(caCertPath, cfg.CACertPEM, 0o640); err2 != nil {
			return result, fmt.Errorf("write CA cert: %w", err2)
		}
	}

	// ─── Server cert (ServerAuth + ClientAuth, 10-year TTL) ──────────────────

	serverCertPath := filepath.Join(cfg.CertDir, "server.crt")
	serverKeyPath := filepath.Join(cfg.CertDir, "server.key")
	serverReason, err := NeedsRotation(serverCertPath, cfg.CACertPEM, []string{cfg.SNIHostname})
	if err != nil {
		return result, fmt.Errorf("check server cert: %w", err)
	}
	if serverReason != "" {
		ca, err := loadCA(cfg)
		if err != nil {
			return result, fmt.Errorf("load CA for server cert: %w", err)
		}
		serverCert, err := IssueServerCert(ca, []string{cfg.SNIHostname})
		if err != nil {
			return result, fmt.Errorf("issue server cert: %w", err)
		}
		if err := writePEM(serverCertPath, serverCert.CertPEM); err != nil {
			return result, fmt.Errorf("write server cert: %w", err)
		}
		if err := writePEM(serverKeyPath, serverCert.KeyPEM); err != nil {
			return result, fmt.Errorf("write server key: %w", err)
		}
		result.ServerRotated = true
	}

	// ─── Ingestor client cert (ClientAuth, 365-day TTL) ──────────────────────

	ingestorCertPath := filepath.Join(cfg.CertDir, "ingestor.crt")
	ingestorKeyPath := filepath.Join(cfg.CertDir, "ingestor.key")
	ingestorReason, err := NeedsRotation(ingestorCertPath, cfg.CACertPEM, nil)
	if err != nil {
		return result, fmt.Errorf("check ingestor cert: %w", err)
	}
	if ingestorReason != "" {
		ca, err := loadCA(cfg)
		if err != nil {
			return result, fmt.Errorf("load CA for ingestor cert: %w", err)
		}
		cn := "controlai-ingest-" + cfg.SiteID
		if len(cn) > 63 {
			cn = cn[:63]
		}
		ingestorCert, err := IssueLeafCert(ca, cn, 365)
		if err != nil {
			return result, fmt.Errorf("issue ingestor cert: %w", err)
		}
		if err := writePEM(ingestorCertPath, ingestorCert.CertPEM); err != nil {
			return result, fmt.Errorf("write ingestor cert: %w", err)
		}
		if err := writePEM(ingestorKeyPath, ingestorCert.KeyPEM); err != nil {
			return result, fmt.Errorf("write ingestor key: %w", err)
		}
		result.IngestorRotated = true
	}

	return result, nil
}

// loadCA reconstructs a CA object from the stored encrypted key and decrypts it.
func loadCA(cfg TrustFileConfig) (*CA, error) {
	ca := &CA{
		CertPEM:      cfg.CACertPEM,
		KeyEncrypted: cfg.CAKeyEnc,
		KeyNonce:     cfg.CAKeyNonce,
	}
	if err := ca.ParseCert(); err != nil {
		return nil, err
	}
	if err := ca.DecryptKey(cfg.MasterKey); err != nil {
		return nil, err
	}
	return ca, nil
}

// writePEM writes PEM bytes atomically to path (tmp + rename).
func writePEM(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
