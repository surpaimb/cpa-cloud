//go:build !windows

package keyprovider

import (
	"context"
	"errors"
)

// Store is deliberately unavailable until a system-protected provider is
// independently verified for this platform.
type Store struct{}

type PreparedVersion struct{}

func Open(string) (*Store, error) { return &Store{}, nil }

func OpenExisting(string) (*Store, error) { return &Store{}, nil }

func (s *Store) Ready() (bool, string) { return false, ReasonUnsupported }

func (s *Store) PrepareVersion(context.Context, string, uint64) (*PreparedVersion, error) {
	return nil, ErrUnavailable
}

func (s *Store) PrepareMaterial(context.Context, Material) (*PreparedVersion, error) {
	return nil, ErrUnavailable
}

func (s *Store) Resolve(context.Context, string, uint64) (*Material, error) {
	return nil, ErrUnavailable
}

func (s *Store) DiscardVersion(context.Context, string, uint64) error { return ErrUnavailable }

func (p *PreparedVersion) Commit() {}

func (p *PreparedVersion) Rollback() error { return errors.New("no key-provider version was prepared") }
