package pki

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// RotationReason enumerates why a cert needs rotation.
type RotationReason string

const (
	ReasonMissing   RotationReason = "missing"
	ReasonExpiring  RotationReason = "expiring_soon"
	ReasonCAMismatch RotationReason = "ca_mismatch"
	ReasonSANDrift  RotationReason = "san_drift"
)

// renewWindowDays is the number of days before expiry that rotation is triggered.
const renewWindowDays = 30

// NeedsRotation checks whether a PEM-encoded cert at certPath needs rotation.
// It returns the applicable reason or an empty string if no rotation is needed.
//
// Rotation is triggered when any of the following hold:
//  1. The file is missing from disk.
//  2. The cert fails verification against caPEM.
//  3. The cert expires within renewWindowDays.
//  4. (Server only) the embedded SANs do not match requiredSANs (nil = skip).
func NeedsRotation(certPath string, caPEM []byte, requiredSANs []string) (RotationReason, error) {
	raw, err := os.ReadFile(certPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ReasonMissing, nil
		}
		return "", fmt.Errorf("read cert %s: %w", certPath, err)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return ReasonMissing, nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ReasonMissing, nil // corrupt → treat as missing
	}

	// Check against CA.
	if caPEM != nil {
		pool := x509.NewCertPool()
		if ok := pool.AppendCertsFromPEM(caPEM); !ok {
			return "", fmt.Errorf("parse CA PEM for verification")
		}
		_, err = cert.Verify(x509.VerifyOptions{Roots: pool})
		if err != nil {
			return ReasonCAMismatch, nil
		}
	}

	// Expiry check.
	if time.Until(cert.NotAfter) <= time.Duration(renewWindowDays)*24*time.Hour {
		return ReasonExpiring, nil
	}

	// SAN drift (server certs only).
	if requiredSANs != nil {
		if !sanSetsEqual(cert.DNSNames, requiredSANs) {
			return ReasonSANDrift, nil
		}
	}

	return "", nil // no rotation needed
}

// sanSetsEqual returns true when the two sets of DNS SANs are equal (order-independent).
func sanSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac, bc := make([]string, len(a)), make([]string, len(b))
	copy(ac, a)
	copy(bc, b)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
