package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"time"
)

// Bound readiness by every loaded serving-chain and trust-anchor certificate.
// This is intentionally conservative for bundles containing unused anchors:
// renew/remove expired material and restart rather than claim fresh TLS trust.
func loadedTLSExpiry(serving tls.Certificate, ca []byte) (time.Time, error) {
	var expiry time.Time
	include := func(c *x509.Certificate) {
		if expiry.IsZero() || c.NotAfter.Before(expiry) {
			expiry = c.NotAfter
		}
	}
	for _, der := range serving.Certificate {
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return time.Time{}, errors.New("engine TLS certificate chain invalid")
		}
		include(certificate)
	}
	// Match CertPool.AppendCertsFromPEM: only successfully parsed, header-free
	// CERTIFICATE blocks contribute trust or a readiness deadline.
	for len(ca) > 0 {
		var block *pem.Block
		block, ca = pem.Decode(ca)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err == nil {
			include(certificate)
		}
	}
	return expiry, nil
}
