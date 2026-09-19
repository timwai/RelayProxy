//go:build windows

package divert

import _ "embed"

// build.ps1 replaces the checked-in placeholder with the signed package for
// the target architecture immediately before building each Windows agent.
//
//go:embed wfp/RelayProxyWfp.zip
var embeddedWFPArchive []byte
