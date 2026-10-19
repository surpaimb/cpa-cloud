//go:build !linux

package offlinerecovery

import "context"

func Run(context.Context, Options) error { return Failure("platform") }
