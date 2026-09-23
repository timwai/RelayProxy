//go:build !windows

package audio

import "context"

func OpenPCMPlayer(context.Context, PCMConfig) (Player, error) {
	return nil, ErrUnavailable
}
