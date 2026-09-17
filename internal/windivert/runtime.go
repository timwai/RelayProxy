// Package windivert describes the pinned, unmodified Windows driver release.
package windivert

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
)

const (
	Version       = "2.2.2"
	ArchiveName   = "WinDivert-2.2.2-A.zip"
	ArchiveURL    = "https://github.com/basil00/WinDivert/releases/download/v2.2.2/" + ArchiveName
	ArchiveSHA256 = "63cb41763bb4b20f600b6de04e991a9c2be73279e317d4d82f237b150c5f3f15"
)

// RuntimeFiles verifies the complete upstream archive before returning only
// the x64 runtime and its license. Archive paths never become output paths.
func RuntimeFiles(data []byte) (map[string][]byte, error) {
	if fmt.Sprintf("%x", sha256.Sum256(data)) != ArchiveSHA256 {
		return nil, fmt.Errorf("WinDivert archive SHA256 mismatch")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	wanted := map[string]string{
		"WinDivert-2.2.2-A/x64/WinDivert.dll":   "WinDivert.dll",
		"WinDivert-2.2.2-A/x64/WinDivert64.sys": "WinDivert64.sys",
		"WinDivert-2.2.2-A/LICENSE":             "LICENSE",
		"WinDivert-2.2.2-A/README":              "README",
		"WinDivert-2.2.2-A/VERSION":             "VERSION",
	}
	files := make(map[string][]byte, len(wanted)+1)
	for _, file := range archive.File {
		name, ok := wanted[file.Name]
		if !ok {
			continue
		}
		if file.UncompressedSize64 == 0 || file.UncompressedSize64 > 1<<20 {
			return nil, fmt.Errorf("unexpected WinDivert file size: %s", file.Name)
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		contents, readErr := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
		closeErr := reader.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if uint64(len(contents)) != file.UncompressedSize64 {
			return nil, fmt.Errorf("incomplete WinDivert file: %s", file.Name)
		}
		files[name] = contents
		delete(wanted, file.Name)
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("WinDivert archive lacks %d required files", len(wanted))
	}
	files["SOURCE.txt"] = []byte("WinDivert " + Version + " (unmodified official x64 binaries)\n" +
		"Source: https://github.com/basil00/WinDivert/tree/v" + Version + "\n" +
		"Archive: " + ArchiveURL + "\nSHA256: " + ArchiveSHA256 + "\n" +
		"License: see LICENSE (LGPLv3 or GPLv2).\n" +
		"A replacement WinDivert.dll and WinDivert64.sys may be placed beside the agent EXE or in its windivert subdirectory.\n")
	return files, nil
}
