//go:build windows && amd64

package divert

import _ "embed"

// Keep the signed upstream archive unchanged. scripts/fetch-windivert.go can
// reproduce this asset, and normal go build also embeds it without extra tags.
//
//go:embed windivert/WinDivert-2.2.2-A.zip
var embeddedWinDivertArchive []byte
