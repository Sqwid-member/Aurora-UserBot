package sysx

import (
	"crypto/x509"
	"os"
	"path/filepath"
)

// RootCertPool returns a cert pool containing system certificates plus Android/Termux
// certificate paths so static Go binaries running on Android don't fail TLS handshakes.
func RootCertPool() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}

	candidateFiles := []string{
		os.Getenv("SSL_CERT_FILE"),
		"/data/data/com.termux/files/usr/etc/tls/cert.pem",
		"/data/data/com.termux/files/usr/etc/ssl/certs/ca-certificates.crt",
		"/data/data/com.termux/files/usr/etc/ssl/cert.pem",
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/ca-bundle.pem",
		"/etc/ssl/cert.pem",
	}
	if prefix := os.Getenv("PREFIX"); prefix != "" {
		candidateFiles = append(candidateFiles,
			filepath.Join(prefix, "etc/tls/cert.pem"),
			filepath.Join(prefix, "etc/ssl/certs/ca-certificates.crt"),
			filepath.Join(prefix, "etc/ssl/cert.pem"),
		)
	}

	for _, path := range candidateFiles {
		if path == "" {
			continue
		}
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			pool.AppendCertsFromPEM(data)
		}
	}

	candidateDirs := []string{
		os.Getenv("SSL_CERT_DIR"),
		"/system/etc/security/cacerts",
		"/apex/com.android.conscrypt/cacerts",
	}
	if prefix := os.Getenv("PREFIX"); prefix != "" {
		candidateDirs = append(candidateDirs, filepath.Join(prefix, "etc/ssl/certs"))
	}

	for _, dir := range candidateDirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(dir, entry.Name())); err == nil && len(data) > 0 {
				pool.AppendCertsFromPEM(data)
			}
		}
	}

	return pool
}
