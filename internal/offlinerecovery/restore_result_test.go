package offlinerecovery

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"cpacloud.local/server/internal/backup"
)

// Error() must not be inspected, joined into the public result, or logged.
type opaqueRestoreCause struct{}

func (opaqueRestoreCause) Error() string { panic("raw restore cause was formatted") }

func TestOfflineRestoreFailurePreservesPrimaryAndCleanup(t *testing.T) {
	primary := &opaqueRestoreCause{}
	cleanupCause := errors.New("private fixture cleanup cause must never escape")
	cleanup := errors.Join(backup.ErrSystemdRestoreCleanupIncomplete, cleanupCause)
	completed := backup.Info{FormatVersion: 3}
	for _, tc := range []struct {
		name            string
		info            backup.Info
		err, contextErr error
		want            string
	}{
		{"success", completed, nil, nil, ""},
		{"restore-only", backup.Info{}, primary, nil, "OFFLINE_V3 restore failed"},
		{"restore-plus-cleanup", backup.Info{}, errors.Join(primary, cleanup), nil, "OFFLINE_V3 restore failed\nOFFLINE_V3 cleanup failed"},
		{"nested-restore-plus-cleanup", backup.Info{}, errors.Join(errors.Join(primary), fmt.Errorf("opaque cleanup wrapper: %w", cleanup)), nil, "OFFLINE_V3 restore failed\nOFFLINE_V3 cleanup failed"},
		{"cancelled-plus-cleanup", backup.Info{}, errors.Join(fmt.Errorf("opaque cancellation wrapper: %w", context.Canceled), cleanup), context.Canceled, "OFFLINE_V3 cancelled failed\nOFFLINE_V3 cleanup failed"},
		{"deadline-plus-cleanup-without-context-state", backup.Info{}, errors.Join(context.DeadlineExceeded, cleanup), nil, "OFFLINE_V3 cancelled failed\nOFFLINE_V3 cleanup failed"},
		{"cancelled-driver-error-plus-cleanup", backup.Info{}, errors.Join(primary, cleanup), context.Canceled, "OFFLINE_V3 cancelled failed\nOFFLINE_V3 cleanup failed"},
		{"cleanup-only-after-completion", completed, errors.Join(nil, cleanup), nil, "OFFLINE_V3 cleanup failed"},
		{"cleanup-only-late-context-cancellation", completed, cleanup, context.Canceled, "OFFLINE_V3 cleanup failed"},
		{"cleanup-without-completion-not-certified", backup.Info{}, cleanup, nil, "OFFLINE_V3 restore failed\nOFFLINE_V3 cleanup failed"},
		{"explicit-cancellation-not-hidden-by-info", completed, errors.Join(context.Canceled, cleanup), nil, "OFFLINE_V3 cancelled failed\nOFFLINE_V3 cleanup failed"},
		{"unexpected-error-with-info-fails-closed", completed, primary, nil, "OFFLINE_V3 restore failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyRestoreFailure(tc.info, tc.err, tc.contextErr)
			if tc.want == "" {
				if got != nil {
					t.Fatal("successful result gained a failure")
				}
				return
			}
			if got == nil || got.Error() != tc.want {
				t.Fatal("fixed stages or primary-first order changed")
			}
			if errors.Is(got, primary) || errors.Is(got, cleanupCause) || errors.Is(got, context.Canceled) || errors.Is(got, context.DeadlineExceeded) || errors.Is(got, backup.ErrSystemdRestoreCleanupIncomplete) {
				t.Fatal("raw cause chain escaped the fixed result boundary")
			}
		})
	}
}
