package auth

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// The certificate is a server certificate and cannot sign others, because the
// easiest way past the browser's warning is to import it as trusted, and one
// that could sign would then be a root authority with its key on this machine.
func TestTheCertificateCannotSignOthers(t *testing.T) {
	certPEM, _, err := selfSigned()
	if err != nil {
		t.Fatalf("selfSigned: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block in the certificate")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	if cert.IsCA {
		t.Error("the certificate says it is a CA")
	}

	if cert.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Error("the certificate may sign certificates")
	}

	if !cert.BasicConstraintsValid {
		t.Error("the certificate does not say it is not a CA, it only leaves it out")
	}

	// And it still works as what it is for: trusted on its own, it verifies for
	// the names it carries. A Go client pinning it is the strictest of the
	// clients that will meet it.
	roots := x509.NewCertPool()
	roots.AddCert(cert)

	if _, err := cert.Verify(x509.VerifyOptions{DNSName: "localhost", Roots: roots}); err != nil {
		t.Errorf("the certificate, trusted on its own, does not verify for localhost: %v", err)
	}
}
