package recoverymaterial

// Independently implemented from docs/adr/0004-portable-backup-recovery-material.md.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cpacloud.local/server/internal/keyprovider"
)

var instanceBindingPurpose = []byte("cpa-cloud/recovery-instance/v1")

var (
	ErrPublishStateUncertain = errors.New("recovery import publish state is uncertain; inspect the target without retrying or overwriting")
	ErrUnsupported           = errors.New("portable key import requires Windows DPAPI")
)

func Export(ctx context.Context, sourceStore, sourceDataDir, output, providerID string, versions []uint64, password []byte) (Reference, error) {
	if ctx == nil {
		return Reference{}, errors.New("context is required")
	}
	reference := Reference{EnvelopeVersion: FormatVersion, ProviderID: providerID, ProviderKind: keyprovider.KindWindowsDPAPIUser, Versions: append([]uint64(nil), versions...)}
	if err := validateReference(reference); err != nil {
		return Reference{}, err
	}
	store, err := keyprovider.OpenExisting(sourceStore)
	if err != nil {
		return Reference{}, fmt.Errorf("open source key provider: %w", err)
	}
	if ready, _ := store.Ready(); !ready {
		return Reference{}, keyprovider.ErrUnavailable
	}
	bindingBefore, err := computeInstanceBinding(sourceDataDir)
	if err != nil {
		return Reference{}, err
	}
	bundle := &Bundle{Reference: reference, InstanceBinding: bindingBefore, Entries: make([]Entry, len(versions))}
	defer bundle.Destroy()
	for index, version := range versions {
		if err := ctx.Err(); err != nil {
			return Reference{}, err
		}
		material, err := store.Resolve(ctx, providerID, version)
		if err != nil {
			return Reference{}, fmt.Errorf("resolve source key-provider version: %w", err)
		}
		bundle.Entries[index].Version = version
		copy(bundle.Entries[index].Key[:], material.Key[:])
		material.Destroy()
	}
	bindingAfter, err := computeInstanceBinding(sourceDataDir)
	if err != nil {
		return Reference{}, err
	}
	if !hmac.Equal(bindingBefore[:], bindingAfter[:]) {
		return Reference{}, errors.New("source instance changed during recovery export")
	}
	encoded, err := Seal(password, bundle)
	if err != nil {
		return Reference{}, err
	}
	defer wipe(encoded)
	if err := writeNewEnvelope(output, encoded); err != nil {
		return Reference{}, err
	}
	return cloneReference(reference), nil
}

func Verify(ctx context.Context, input string, password []byte) (Reference, error) {
	if ctx == nil {
		return Reference{}, errors.New("context is required")
	}
	bundle, err := readBundle(ctx, input, password)
	if err != nil {
		return Reference{}, err
	}
	defer bundle.Destroy()
	return cloneReference(bundle.Reference), nil
}

func readBundle(ctx context.Context, input string, password []byte) (*Bundle, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := readBoundedRegularFile(input, maxEnvelopeSize)
	if err != nil {
		return nil, fmt.Errorf("read recovery material: %w", err)
	}
	defer wipe(encoded)
	bundle, err := Open(password, encoded)
	if err != nil {
		return nil, err
	}
	return bundle, nil
}

func computeInstanceBinding(dataDir string) ([32]byte, error) {
	var result [32]byte
	directory, err := cleanPath(dataDir)
	if err != nil {
		return result, fmt.Errorf("source data directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("source data directory is not a supported real directory")
	}
	if reparse, err := recoveryPathIsReparsePoint(directory); err != nil || reparse {
		if err != nil {
			return result, err
		}
		return result, errors.New("source data directory cannot be a reparse point")
	}
	keyBytes, err := readBoundedRegularFile(filepath.Join(directory, "master.key"), 4096)
	if err != nil {
		return result, fmt.Errorf("read source instance binding: %w", err)
	}
	defer wipe(keyBytes)
	mac := hmac.New(sha256.New, keyBytes)
	_, _ = mac.Write(instanceBindingPurpose)
	copy(result[:], mac.Sum(nil))
	return result, nil
}

func readBoundedRegularFile(path string, limit int) ([]byte, error) {
	cleaned, err := cleanPath(path)
	if err != nil {
		return nil, err
	}
	if err := validateRecoveryDirectoryChain(filepath.Dir(cleaned)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > int64(limit) {
		return nil, errors.New("path is not a supported bounded regular file")
	}
	if reparse, err := recoveryPathIsReparsePoint(cleaned); err != nil || reparse {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("path cannot be a reparse point")
	}
	value, err := os.ReadFile(cleaned)
	if err != nil {
		return nil, err
	}
	current, err := os.Lstat(cleaned)
	if err != nil || !os.SameFile(info, current) || current.Size() != int64(len(value)) {
		wipe(value)
		return nil, errors.New("path changed while it was being read")
	}
	return value, nil
}

func writeNewEnvelope(output string, encoded []byte) (returnErr error) {
	target, err := cleanPath(output)
	if err != nil {
		return fmt.Errorf("recovery material output: %w", err)
	}
	if filepath.Dir(target) == target {
		return errors.New("recovery material output cannot be a filesystem root")
	}
	if _, err := os.Lstat(target); err == nil {
		return errors.New("recovery material output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(target)
	if err := validateRecoveryDirectoryChain(parent); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(parent, ".cpa-cloud-recovery-material-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		if returnErr != nil && published {
			_ = os.Remove(target)
		}
	}()
	if err := hardenRecoveryPath(temporaryPath, false); err != nil {
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, target); err != nil {
		if _, statErr := os.Lstat(target); statErr == nil {
			return errors.New("recovery material output already exists")
		}
		return err
	}
	published = true
	if err := syncRecoveryDirectory(parent); err != nil {
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	if err := syncRecoveryDirectory(parent); err != nil {
		return err
	}
	published = false
	return nil
}

func cleanPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func validateRecoveryDirectoryChain(path string) error {
	absolute, err := cleanPath(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(os.PathSeparator)
	relative := strings.TrimPrefix(absolute, current)
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("path contains a link or non-directory component")
		}
		if reparse, err := recoveryPathIsReparsePoint(current); err != nil || reparse {
			if err != nil {
				return err
			}
			return errors.New("path contains a reparse point")
		}
	}
	return nil
}

func cloneReference(reference Reference) Reference {
	reference.Versions = append([]uint64(nil), reference.Versions...)
	return reference
}
