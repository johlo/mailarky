package sandbox

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCertificateEndpointServesListenerCertificates(t *testing.T) {
	c := defaultConfig()
	c.Cert, c.Key = "../../testdata/tls/server.crt", "../../testdata/tls/server.key"
	_, handler := testManager(t, c)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/certificate", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-pem-file" {
		t.Fatalf("status %d, type %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	body := rec.Body.Bytes()
	if bytes.Contains(body, []byte("PRIVATE KEY")) {
		t.Fatal("certificate bundle contains a private key")
	}
	want, err := os.ReadFile(c.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(certificateDER(t, body), certificateDER(t, want)) {
		t.Fatal("served certificate differs from the configured one")
	}

	// A client trusting only the served bundle completes a handshake with the IMAP certificate.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(body) {
		t.Fatal("bundle is not usable as a CA file")
	}
	pair, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		if conn, err := listener.Accept(); err == nil {
			_ = conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: pool, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("handshake trusting the served bundle: %v", err)
	}
	conn.Close()
}

func TestCertificateEndpointIncludesSeparateSMTPCertificate(t *testing.T) {
	c := defaultConfig()
	c.Cert, c.Key = "../../testdata/tls/server.crt", "../../testdata/tls/server.key"
	c.SMTPTLS = "starttls"
	c.SMTPCert, c.SMTPKey = writeTestCertificate(t)
	_, handler := testManager(t, c)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/certificate", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if n := bytes.Count(rec.Body.Bytes(), []byte("BEGIN CERTIFICATE")); n != 2 {
		t.Fatalf("bundle has %d certificates, want IMAP and SMTP", n)
	}

	// The same file for SMTP and IMAP appears once.
	c.SMTPCert, c.SMTPKey = c.Cert, c.Key
	bundle, err := certificateBundle(c)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(bundle, []byte("BEGIN CERTIFICATE")); n != 1 {
		t.Fatalf("bundle has %d certificates for one shared certificate", n)
	}
}

func certificateDER(t *testing.T, data []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("no PEM certificate in %q", data)
	}
	return block.Bytes
}

func writeTestCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "smtp.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "smtp.crt"), filepath.Join(dir, "smtp.key")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return certPath, keyPath
}
