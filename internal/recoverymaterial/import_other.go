//go:build !windows

package recoverymaterial

import (
	"context"
)

func Import(context.Context, string, string, string, []byte) (Reference, error) {
	return Reference{}, ErrUnsupported
}
