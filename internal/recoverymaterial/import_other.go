//go:build !windows

package recoverymaterial

import (
	"context"
	"errors"
)

func Import(ctx context.Context, _, _, _ string, _ []byte) (Reference, error) {
	if ctx == nil {
		return Reference{}, errors.New("context is required")
	}
	return Reference{}, ErrUnsupported
}
