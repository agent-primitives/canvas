package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func renewedCertificate(t *testing.T, f *tlsFixture, until time.Time) []byte {
	t.Helper()
	leaf, err := x509.ParseCertificate(f.certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	leaf.NotAfter = until
	key := f.certificate.PrivateKey.(*ecdsa.PrivateKey)
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
func TestReadinessExpiresWithLoadedTLSMaterial(t *testing.T) {
	for _, kind := range []string{"serving-leaf", "serving-intermediate", "sessions-ca"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t, nil, true, false)
			now := time.Now()
			cutoff := now.Add(30 * time.Minute).Truncate(time.Second)
			short := renewedCertificate(t, f, cutoff)
			target := f.config.TLSCertFile
			if kind == "sessions-ca" {
				target = f.config.SessionsCAFile
			}
			original, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			material := short
			if kind == "serving-intermediate" || kind == "sessions-ca" {
				material = append(append([]byte(nil), original...), short...)
			}
			if err = os.WriteFile(target, material, 0600); err != nil {
				t.Fatal(err)
			}
			var nanos atomic.Int64
			nanos.Store(now.UnixNano())
			clock := func() time.Time { return time.Unix(0, nanos.Load()) }
			e, err := startEngineWithClock(t.Context(), f.config, clock)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if err = healthProbe(e.readinessAddress); err != nil {
				t.Fatal("not ready before expiry", err)
			}
			nanos.Store(cutoff.Add(-time.Nanosecond).UnixNano())
			if err = healthProbe(e.readinessAddress); err != nil {
				t.Fatal("not ready before boundary", err)
			}
			nanos.Store(cutoff.UnixNano())
			hc := &http.Client{Timeout: time.Second}
			res, err := hc.Get("http://" + e.readinessAddress + "/readyz")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 503 || string(body) == readyBody || res.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("expired TLS reports ready: %d %s", res.StatusCode, body)
			}
			if err = healthProbe(e.readinessAddress); err == nil {
				t.Fatal("health probe accepted expired TLS")
			}
			// Projected-file renewal alone does not refresh material already in memory.
			if err = os.WriteFile(target, original, 0600); err != nil {
				t.Fatal(err)
			}
			nanos.Store(cutoff.Add(time.Minute).UnixNano())
			if err = healthProbe(e.readinessAddress); err == nil {
				t.Fatal("readiness silently reloaded rotated material")
			}
			if f.calls.Load() != 0 || f.exec.Load() != 0 || f.resume.Load() != 0 {
				t.Fatal("readiness dispatched work")
			}
		})
	}
}

func TestMalformedServingIntermediateRefusesBeforeState(t *testing.T) {
	f := fixture(t, nil, true, false)
	original, err := os.ReadFile(f.config.TLSCertFile)
	if err != nil {
		t.Fatal(err)
	}
	material := append(original, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid chain certificate")})...)
	if err = os.WriteFile(f.config.TLSCertFile, material, 0600); err != nil {
		t.Fatal(err)
	}
	if e, err := startEngine(t.Context(), f.config); err == nil {
		e.Close()
		t.Fatal("malformed intermediate accepted")
	}
	if _, err = os.Lstat(f.config.StateDirectory); !os.IsNotExist(err) {
		t.Fatal("invalid chain touched state", err)
	}
}

func TestReadinessIgnoresUntrustedPEMBlocks(t *testing.T) {
	f := fixture(t, nil, true, false)
	now := time.Now()
	short := renewedCertificate(t, f, now.Add(time.Minute))
	block, _ := pem.Decode(short)
	original, err := os.ReadFile(f.config.SessionsCAFile)
	if err != nil {
		t.Fatal(err)
	}
	material := append(original, pem.EncodeToMemory(&pem.Block{Type: "OTHER", Bytes: block.Bytes})...)
	material = append(material, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"ignored": "true"}, Bytes: block.Bytes})...)
	material = append(material, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid")})...)
	if err = os.WriteFile(f.config.SessionsCAFile, material, 0600); err != nil {
		t.Fatal(err)
	}
	var nanos atomic.Int64
	nanos.Store(now.UnixNano())
	e, err := startEngineWithClock(t.Context(), f.config, func() time.Time { return time.Unix(0, nanos.Load()) })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	nanos.Store(now.Add(2 * time.Minute).UnixNano())
	if err = healthProbe(e.readinessAddress); err != nil {
		t.Fatal("untrusted PEM block shortened readiness", err)
	}
}
