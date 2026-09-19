//go:build windows

package divert

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedWFPPackageRoundTrip(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range map[string][]byte{
		"RelayProxyWfp.sys": image,
		"RelayProxyWfp.inf": []byte("[Version]\nCatalogFile=RelayProxyWfp.cat\n[DefaultInstall.Services]\nAddService=RelayProxyWfp,0x2,X\nRelayProxyWfp.sys\n"),
		"RelayProxyWfp.cat": []byte("catalog-fixture"),
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	files, err := parseEmbeddedWFPPackage(buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(files["RelayProxyWfp.sys"], image) {
		t.Fatal("embedded driver bytes changed")
	}
}

func TestEmbeddedWFPPackageRejectsPlaceholder(t *testing.T) {
	if _, err := parseEmbeddedWFPPackage([]byte("RelayProxy WFP payload placeholder")); err == nil {
		t.Fatal("placeholder accepted as driver package")
	}
}

func TestEmbeddedWFPPackageRejectsExtraFile(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range map[string][]byte{
		"RelayProxyWfp.sys": image,
		"RelayProxyWfp.inf": []byte("CatalogFile=RelayProxyWfp.cat\nAddService=RelayProxyWfp\nRelayProxyWfp.sys"),
		"RelayProxyWfp.cat": []byte("cat"),
		filepath.Join("unexpected", "payload.bin"): []byte("bad"),
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := parseEmbeddedWFPPackage(buffer.Bytes()); err == nil {
		t.Fatal("unexpected package entry accepted")
	}
}
