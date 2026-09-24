//go:build !windows

package keyprovider

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupportedPlatformFailsClosedWithoutFilesystemWrites(t *testing.T) {
	root := t.TempDir() + "/must-not-be-created"
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if ready, reason := store.Ready(); ready || reason != ReasonUnsupported {
		t.Fatalf("ready=%v reason=%q", ready, reason)
	}
	if _, err := store.PrepareVersion(context.Background(), "bkp_test", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("prepare error=%v", err)
	}
	if _, err := store.Resolve(context.Background(), "bkp_test", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("resolve error=%v", err)
	}
}
