package sandbox

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"net/http"
)

// certificate serves the certificates of the TLS listeners as one PEM bundle,
// so clients can trust them without access to the server's files. Only the
// public certificates are written; private keys never leave the server.
func (a *httpAPI) certificate(w http.ResponseWriter, r *http.Request) {
	bundle, err := certificateBundle(a.service.config)
	if err != nil {
		apiError(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="mailarky.crt"`)
	_, _ = w.Write(bundle)
}

func certificateBundle(c configuration) ([]byte, error) {
	pairs := [][2]string{{c.Cert, c.Key}}
	if c.SMTPTLS != "" && c.SMTPCert != "" {
		pairs = append(pairs, [2]string{c.SMTPCert, c.SMTPKey})
	}
	if c.HTTPCert != "" {
		pairs = append(pairs, [2]string{c.HTTPCert, c.HTTPKey})
	}
	var out bytes.Buffer
	seen := map[string]bool{}
	for _, pair := range pairs {
		// Load the pair as the listeners do, so the bundle matches what they present.
		cert, err := tls.LoadX509KeyPair(pair[0], pair[1])
		if err != nil {
			return nil, fmt.Errorf("load TLS certificate %s: %w", pair[0], err)
		}
		for _, der := range cert.Certificate {
			if seen[string(der)] {
				continue
			}
			seen[string(der)] = true
			if err := pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
				return nil, err
			}
		}
	}
	return out.Bytes(), nil
}
