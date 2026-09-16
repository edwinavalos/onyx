package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CA signs the leaf certificates the forward proxy presents when it
// terminates TLS for an intercepted host. One CA lives per Onyx root
// (proxy-ca.crt / proxy-ca.key, the key readable by the user only); its
// certificate is public and is what guests are told to trust.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

const (
	caCertFile = "proxy-ca.crt"
	caKeyFile  = "proxy-ca.key"
	caLifetime = 10 * 365 * 24 * time.Hour
	// Leaves are short-lived and re-minted; the guest only ever sees them
	// for the duration of one VM's life.
	leafLifetime = 7 * 24 * time.Hour
)

// LoadOrCreateCA returns the CA stored in dir, creating it on first use.
func LoadOrCreateCA(dir string) (*CA, error) {
	certPath := filepath.Join(dir, caCertFile)
	keyPath := filepath.Join(dir, caKeyFile)
	certPEM, err1 := os.ReadFile(certPath) // #nosec G304 -- fixed name under the Onyx root
	keyPEM, err2 := os.ReadFile(keyPath)   // #nosec G304 -- fixed name under the Onyx root
	if err1 == nil && err2 == nil {
		return parseCA(certPEM, keyPEM)
	}
	if !errors.Is(err1, os.ErrNotExist) && err1 != nil {
		return nil, err1
	}
	if !errors.Is(err2, os.ErrNotExist) && err2 != nil {
		return nil, err2
	}
	ca, certPEM, keyPEM, err := newCA()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	err = os.WriteFile(certPath, certPEM, 0o644) // #nosec G306 -- public certificate
	if err != nil {
		return nil, err
	}
	return ca, nil
}

func newCA() (*CA, []byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Onyx credential proxy CA", Organization: []string{"Onyx"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	ca, err := parseCA(certPEM, keyPEM)
	return ca, certPEM, keyPEM, err
}

func parseCA(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, errors.New("proxy ca: malformed PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("proxy ca: %w", err)
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("proxy ca key: %w", err)
	}
	return &CA{cert: cert, key: key, pem: certPEM, leaves: map[string]*tls.Certificate{}}, nil
}

// PEM is the CA certificate, for the guest's trust store.
func (c *CA) PEM() []byte { return c.pem }

// Leaf returns a certificate for host signed by the CA, minting it on
// first use and again once it nears expiry.
func (c *CA) Leaf(host string) (*tls.Certificate, error) {
	host = strings.ToLower(host)
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.leaves[host]; ok && time.Until(l.Leaf.NotAfter) > leafLifetime/2 {
		return l, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	l := &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key, Leaf: leaf}
	c.leaves[host] = l
	return l, nil
}

// TLSConfig terminates TLS for whatever host the client names via SNI,
// falling back to fallbackHost (the CONNECT target) when there is none.
func (c *CA) TLSConfig(fallbackHost string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			host := hello.ServerName
			if host == "" {
				host = fallbackHost
			}
			return c.Leaf(host)
		},
	}
}
