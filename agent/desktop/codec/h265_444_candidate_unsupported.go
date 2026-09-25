//go:build !windows || !amd64

package codec

import "context"

func probePlatformH265444RuntimeCandidates(context.Context) []H265444RuntimeCandidate {
	return nil
}
