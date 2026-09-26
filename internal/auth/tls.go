package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnsureCertificate loads the interface's TLS certificate, generating a self
// signed one on first start.
//
// TLS is not optional once there is a password. The interface is meant to be
// reachable from a phone, and a password sent in clear text over the home
// network would be worse than no password at all, because it would look like
// protection.
//
// Self signed, so the browser asks once per machine. That is the same thing
// Sunshine does, and the seats already train people to accept it: their web
// interfaces are the https://<seat>:47990 links on the seat cards.
//
// Generated once and then kept. Regenerating on every start, or whenever an
// address changes, would throw away the exception the browser has stored and
// ask again every time, which is how people learn to click through warnings
// without reading them.
func EnsureCertificate(stateDir string) (tls.Certificate, error) {
	dir := filepath.Join(stateDir, "tls")
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		return cert, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}

	certPEM, keyPEM, err := selfSigned()
	if err != nil {
		return tls.Certificate{}, err
	}

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}

	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}

	return tls.X509KeyPair(certPEM, keyPEM)
}

func selfSigned() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	hostname, _ := os.Hostname()

	names := []string{"localhost"}
	if hostname != "" {
		names = append(names, hostname)
	}

	// A server certificate and nothing more: not a CA, and not allowed to sign
	// certificates. It used to be both, and that is harmless for as long as
	// people only click through the warning. It stops being harmless the
	// moment somebody does the tidier thing and imports it as trusted, which is
	// what the warning invites: they have then installed a root authority
	// whose key sits on this machine, and anybody who ever reads key.pem can
	// issue a certificate for any site on the internet that their browser
	// accepts without a word.
	//
	// Browsers accept a self signed leaf the same way, after the same one
	// click, and Go's verifier accepts one placed in RootCAs, which the test
	// checks. KeyEncipherment went with it: it describes RSA key transport and
	// means nothing for an ECDSA key.
	//
	// Certificates already on disk are left alone. EnsureCertificate keeps what
	// it finds, which is deliberate, and replacing every installation's
	// certificate to close a door that only opens if somebody imported it
	// would make every browser ask again, on every machine, at once.
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Polyseat", Organization: []string{"Polyseat"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              names,
		IPAddresses:           localAddresses(),
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

// localAddresses collects the addresses the interface might be reached on, so
// the certificate at least matches when somebody types one in.
func localAddresses() []net.IP {
	out := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}

	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.IsGlobalUnicast() {
				out = append(out, ipnet.IP)
			}
		}
	}

	return out
}

// Fingerprint returns the certificate's SHA-256 fingerprint in the form
// browsers show it, so somebody can compare what they are being asked to trust
// against what the daemon logged.
func Fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}

	sum := sha256.Sum256(cert.Certificate[0])

	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}

	return strings.Join(parts, ":")
}
