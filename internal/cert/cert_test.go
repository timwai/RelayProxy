package cert

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredCertificateCannotFallBackOrOverwriteFiles(t *testing.T) {
	for _, name := range []string{"missing both", "missing certificate", "missing key", "certificate only", "key only", "invalid pair", "certificate directory"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			certPath, keyPath := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
			originals := map[string][]byte{}
			write := func(path, value string) {
				t.Helper()
				originals[path] = []byte(value)
				if err := os.WriteFile(path, originals[path], 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "missing certificate":
				write(keyPath, "existing private key must not be replaced")
			case "missing key":
				write(certPath, "existing certificate must not be replaced")
			case "certificate only":
				write(certPath, "existing certificate must not be replaced")
				keyPath = ""
			case "key only":
				write(keyPath, "existing private key must not be replaced")
				certPath = ""
			case "invalid pair":
				write(certPath, "not a certificate")
				write(keyPath, "not a private key")
			case "certificate directory":
				if err := os.Mkdir(certPath, 0700); err != nil {
					t.Fatal(err)
				}
				write(keyPath, "existing private key must not be replaced")
			}
			certificate, err := EnsureCertificate(certPath, keyPath, "must-not-generate.test")
			if err == nil || len(certificate.Certificate) != 0 {
				t.Fatalf("configured pair was replaced by a self-signed certificate: %v", err)
			}
			for _, path := range []string{certPath, keyPath} {
				if path == "" || (name == "certificate directory" && path == certPath) {
					continue
				}
				data, err := os.ReadFile(path)
				if original, existed := originals[path]; existed {
					if err != nil || !bytes.Equal(original, data) {
						t.Fatalf("existing file was changed: %s, %v", path, err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("missing configured file was created: %s, %v", path, err)
				}
			}
		})
	}
}

func TestConfiguredCertificateAndKeyAreUsedUnmodified(t *testing.T) {
	fixture, err := EnsureCertificate("", "", "configured.test")
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(fixture.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.Certificate[0]})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	for path, data := range map[string][]byte{certPath: certPEM, keyPath: keyPEM} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := EnsureCertificate(certPath, keyPath, "must-not-generate.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Certificate) != 1 || !bytes.Equal(loaded.Certificate[0], fixture.Certificate[0]) {
		t.Fatal("configured leaf certificate was not used")
	}
	for path, expected := range map[string][]byte{certPath: certPEM, keyPath: keyPEM} {
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("loading changed %s: %v", path, err)
		}
	}

	other, err := EnsureCertificate("", "", "unrelated.test")
	if err != nil {
		t.Fatal(err)
	}
	otherDER, err := x509.MarshalPKCS8PrivateKey(other.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	otherPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: otherDER})
	if err := os.WriteFile(keyPath, otherPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCertificate(certPath, keyPath, "must-not-generate.test"); err == nil {
		t.Fatal("mismatched private key was accepted")
	}
	if actual, err := os.ReadFile(keyPath); err != nil || !bytes.Equal(actual, otherPEM) {
		t.Fatalf("mismatched private key was overwritten: %v", err)
	}
}
