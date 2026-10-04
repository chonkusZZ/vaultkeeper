package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// loadOrCreateCert returns a self-signed certificate valid for host (IP or
// DNS name). It is regenerated when host changes. The PEM is shared with the
// manager so source agents can pin it via restic's --cacert.
func loadOrCreateCert(dir, host string) (tls.Certificate, string, error) {
	certPath := filepath.Join(dir, "data-cert.pem")
	keyPath := filepath.Join(dir, "data-key.pem")
	hostPath := filepath.Join(dir, "data-cert.host")
	if h, err := os.ReadFile(hostPath); err == nil && string(h) == host {
		if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
			pemBytes, _ := os.ReadFile(certPath)
			return c, string(pemBytes), nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "vaultkeeper-agent"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = append(tpl.IPAddresses, ip)
	} else if host != "" {
		tpl.DNSNames = append(tpl.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kb, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	_ = os.WriteFile(certPath, certPEM, 0o644)
	_ = os.WriteFile(keyPath, keyPEM, 0o600)
	_ = os.WriteFile(hostPath, []byte(host), 0o644)
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	return c, string(certPEM), err
}
