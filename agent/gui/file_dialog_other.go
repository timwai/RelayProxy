//go:build !darwin && !windows

package gui

import (
	"context"
	"errors"
)

func nativeChooseShareFolder(context.Context) (string, error) {
	return "", errors.New("native directory picker unavailable on browser UI for this platform")
}
func nativeChooseDownloadPath(context.Context, string) (string, error) {
	return "", errors.New("native Save As picker unavailable on browser UI for this platform")
}
