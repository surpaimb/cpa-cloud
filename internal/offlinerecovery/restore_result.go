package offlinerecovery

// Independently authored correction for the fixed-stage result boundary.
// See docs/linux-systemd-offline-recovery-cli-contract.md; no raw cause escapes.
import (
	"context"
	"errors"

	"cpacloud.local/server/internal/backup"
)

func classifyRestoreFailure(info backup.Info, err, contextErr error) error {
	if err == nil {
		return nil
	}
	cleanup := errors.Is(err, backup.ErrSystemdRestoreCleanupIncomplete)
	cancelledCause := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	// The current v3 API returns completed Info only after publish succeeds;
	// its deferred stage cleanup can then append an error to that success.
	// A target's mere existence is not proof of this completed API result.
	var result error
	if info.FormatVersion != 3 || !cleanup || cancelledCause {
		stage := "restore"
		if cancelledCause || contextErr != nil {
			stage = "cancelled"
		}
		result = Failure(stage)
	}
	if cleanup {
		result = errors.Join(result, Failure("cleanup"))
	}
	return result
}
