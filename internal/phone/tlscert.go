package phone

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// The server's certificate is self-signed: the phone pins its SHA-256
// fingerprint at pairing, whose code is derived from it (pairing.go), so
// no CA (and no hostname) is involved.
// It is made once and kept, so a pinned phone keeps working.

const (
	certFile = "cert.pem"
	keyFile  = "key.pem"
	// certYears: the pin is the trust, so expiry would only break
	// paired phones; a new certificate means pairing again.
	certYears = 20
)

// loadOrCreateCert reads dir's certificate and key, making them on first
// use (key mode 0600).
func loadOrCreateCert(dir string, now time.Time) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
	if err == nil {
		return cert, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return tls.Certificate{}, fmt.Errorf("phone certificate: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Musubi phone access"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(certYears, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"musubi.local"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	// The key first: a certificate without its key would be useless and
	// would block making a new pair.
	if err := writePrivate(filepath.Join(dir, keyFile), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
		return tls.Certificate{}, err
	}
	if err := writePrivate(filepath.Join(dir, certFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
}

// Fingerprint is the SHA-256 of a certificate's DER bytes, lowercase hex:
// what the phone pins.
func Fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}

// writePrivate writes b to path (mode 0600) through a temporary file, so
// a crash never leaves half a file.
func writePrivate(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
