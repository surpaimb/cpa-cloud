//go:build windows

package recoverymaterial

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/keyprovider"
	"golang.org/x/sys/windows"
	_ "modernc.org/sqlite"
)

type importHooks struct {
	beforePublish func() error
	syncParent    func(string) error
}

func Import(ctx context.Context, input, backupPackage, targetRoot string, password []byte) (Reference, error) {
	return importWithHooks(ctx, input, backupPackage, targetRoot, password, importHooks{syncParent: syncRecoveryDirectory})
}

func importWithHooks(ctx context.Context, input, backupPackage, targetRoot string, password []byte, hooks importHooks) (_ Reference, returnErr error) {
	target, parent, parentIdentity, err := validateNewRecoveryTarget(targetRoot)
	if err != nil {
		return Reference{}, err
	}
	bundle, err := readBundle(ctx, input, password)
	if err != nil {
		return Reference{}, err
	}
	defer bundle.Destroy()
	backupReference, err := backup.InspectKeyReference(ctx, backupPackage)
	if err != nil {
		return Reference{}, err
	}
	restoreMaterial, ok := bundleMaterial(bundle, backupReference)
	if !ok {
		return Reference{}, errors.New("recovery material does not contain the backup key version")
	}
	defer wipe(restoreMaterial.WrappingKey[:])
	stage, err := os.MkdirTemp(parent, ".cpa-cloud-key-import-")
	if err != nil {
		return Reference{}, err
	}
	if err := hardenRecoveryPath(stage, true); err != nil {
		_ = os.Remove(stage)
		return Reference{}, err
	}
	stageIdentity, err := os.Lstat(stage)
	if err != nil {
		_ = os.Remove(stage)
		return Reference{}, err
	}
	var store *keyprovider.Store
	var dataIdentity, providerIdentity os.FileInfo
	published := false
	defer func() {
		if returnErr == nil || published {
			return
		}
		if cleanupErr := cleanupRecoveryStage(ctx, stage, stageIdentity, dataIdentity, providerIdentity, store, bundle.Reference); cleanupErr != nil {
			returnErr = fmt.Errorf("recovery import cleanup failed: %w", cleanupErr)
		}
	}()
	dataDir := filepath.Join(stage, "data")
	if _, err := backup.RestoreWithKeyMaterial(ctx, backupPackage, dataDir, restoreMaterial); err != nil {
		return Reference{}, err
	}
	dataIdentity, err = os.Lstat(dataDir)
	if err != nil {
		return Reference{}, err
	}
	stagedBinding, err := computeInstanceBinding(dataDir)
	if err != nil {
		return Reference{}, err
	}
	if !constantTimeBindingEqual(stagedBinding, bundle.InstanceBinding) {
		return Reference{}, errors.New("recovery material belongs to a different CPA Cloud instance")
	}
	activeVersion, err := validateRestoredProvider(ctx, filepath.Join(dataDir, "cpa-cloud.db"), bundle.Reference)
	if err != nil {
		return Reference{}, err
	}
	if !containsVersion(bundle.Reference.Versions, activeVersion) {
		return Reference{}, errors.New("recovery material does not contain the restored active key version")
	}
	providerStore := filepath.Join(stage, "provider-store")
	store, err = keyprovider.Open(providerStore)
	if err != nil {
		return Reference{}, err
	}
	providerIdentity, err = os.Lstat(providerStore)
	if err != nil {
		return Reference{}, err
	}
	for index := range bundle.Entries {
		if err := ctx.Err(); err != nil {
			return Reference{}, err
		}
		entry := &bundle.Entries[index]
		material := keyprovider.Material{ProviderID: bundle.Reference.ProviderID, Kind: bundle.Reference.ProviderKind, Version: entry.Version}
		copy(material.Key[:], entry.Key[:])
		prepared, err := store.PrepareMaterial(ctx, material)
		material.Destroy()
		if err != nil {
			return Reference{}, err
		}
		prepared.Commit()
	}
	if hooks.beforePublish != nil {
		if err := hooks.beforePublish(); err != nil {
			return Reference{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Reference{}, err
	}
	if err := validateRecoveryDirectoryChain(parent); err != nil {
		return Reference{}, err
	}
	for _, known := range []struct {
		path     string
		identity os.FileInfo
	}{{stage, stageIdentity}, {dataDir, dataIdentity}, {providerStore, providerIdentity}} {
		if err := validateKnownRecoveryDirectory(known.path, known.identity); err != nil {
			return Reference{}, err
		}
	}
	currentParent, err := os.Lstat(parent)
	if err != nil || !os.SameFile(parentIdentity, currentParent) {
		return Reference{}, errors.New("recovery target parent changed during import")
	}
	if _, err := os.Lstat(target); err == nil {
		return Reference{}, errors.New("recovery target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Reference{}, err
	}
	if err := os.Rename(stage, target); err != nil {
		return Reference{}, fmt.Errorf("publish recovery target without overwrite: %w", err)
	}
	published = true
	if hooks.syncParent == nil {
		hooks.syncParent = syncRecoveryDirectory
	}
	if err := hooks.syncParent(parent); err != nil {
		return Reference{}, ErrPublishStateUncertain
	}
	return cloneReference(bundle.Reference), nil
}

func validateNewRecoveryTarget(value string) (string, string, os.FileInfo, error) {
	target, err := cleanPath(value)
	if err != nil {
		return "", "", nil, err
	}
	if strings.HasPrefix(target, `\\`) || strings.HasPrefix(target, `//`) || strings.HasPrefix(target, `\\?\`) || strings.HasPrefix(target, `\\.\`) {
		return "", "", nil, errors.New("recovery target must be on a local fixed volume")
	}
	volume := filepath.VolumeName(target)
	if len(volume) != 2 || volume[1] != ':' || filepath.Dir(target) == target {
		return "", "", nil, errors.New("recovery target must be a non-root local drive path")
	}
	rootPointer, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil || windows.GetDriveType(rootPointer) != windows.DRIVE_FIXED {
		return "", "", nil, errors.New("recovery target must be on a local fixed volume")
	}
	if _, err := os.Lstat(target); err == nil {
		return "", "", nil, errors.New("recovery target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", nil, err
	}
	parent := filepath.Dir(target)
	if err := validateRecoveryDirectoryChain(parent); err != nil {
		return "", "", nil, err
	}
	identity, err := os.Lstat(parent)
	if err != nil {
		return "", "", nil, err
	}
	return target, parent, identity, nil
}

func bundleMaterial(bundle *Bundle, reference backup.KeyReference) (backup.KeyMaterial, bool) {
	if bundle.Reference.ProviderID != reference.ID || bundle.Reference.ProviderKind != reference.Kind {
		return backup.KeyMaterial{}, false
	}
	for index := range bundle.Entries {
		if bundle.Entries[index].Version == reference.Version {
			material := backup.KeyMaterial{ProviderID: reference.ID, ProviderKind: reference.Kind, ProviderVersion: reference.Version}
			copy(material.WrappingKey[:], bundle.Entries[index].Key[:])
			return material, true
		}
	}
	return backup.KeyMaterial{}, false
}

func validateRestoredProvider(ctx context.Context, databasePath string, reference Reference) (uint64, error) {
	database, err := sql.Open("sqlite", recoverySQLiteURI(databasePath))
	if err != nil {
		return 0, err
	}
	defer database.Close()
	var kind string
	var version uint64
	if err := database.QueryRowContext(ctx, `SELECT kind,active_version FROM backup_key_providers WHERE id=?`, reference.ProviderID).Scan(&kind, &version); err != nil {
		return 0, errors.New("restored database does not contain the recovery key provider")
	}
	if kind != reference.ProviderKind || version == 0 || version > maxPublicVersion {
		return 0, errors.New("restored key provider does not match recovery material")
	}
	return version, nil
}

func recoverySQLiteURI(path string) string {
	absolute, _ := filepath.Abs(path)
	slash := filepath.ToSlash(absolute)
	if runtime.GOOS == "windows" && filepath.VolumeName(absolute) != "" {
		slash = "/" + slash
	}
	uri := url.URL{Scheme: "file", Path: slash}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	uri.RawQuery = query.Encode()
	return uri.String()
}

func cleanupRecoveryStage(ctx context.Context, stage string, stageIdentity, dataIdentity, providerIdentity os.FileInfo, store *keyprovider.Store, reference Reference) error {
	if stage == "" {
		return nil
	}
	if err := validateKnownRecoveryDirectory(stage, stageIdentity); err != nil {
		return err
	}
	dataDir := filepath.Join(stage, "data")
	if err := validateOptionalKnownRecoveryDirectory(dataDir, dataIdentity); err != nil {
		return err
	}
	providerStore := filepath.Join(stage, "provider-store")
	if err := validateOptionalKnownRecoveryDirectory(providerStore, providerIdentity); err != nil {
		return err
	}
	if store != nil {
		for _, version := range reference.Versions {
			if err := store.DiscardVersion(ctx, reference.ProviderID, version); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if err := os.Remove(providerStore); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, name := range []string{"cpa-cloud.db-wal", "cpa-cloud.db-shm", "cpa-cloud.db-journal", "cpa-cloud.db", "master.key"} {
		if err := removeKnownRecoveryFile(filepath.Join(dataDir, name)); err != nil {
			return err
		}
	}
	if err := os.Remove(dataDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(stage); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validateOptionalKnownRecoveryDirectory(path string, identity os.FileInfo) error {
	if identity == nil {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("recovery stage contains an unexpected directory")
	}
	return validateKnownRecoveryDirectory(path, identity)
}

func validateKnownRecoveryDirectory(path string, identity os.FileInfo) error {
	if identity == nil {
		return errors.New("recovery stage directory identity is unavailable")
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(identity, current) || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return errors.New("recovery stage directory identity changed")
	}
	if reparse, err := recoveryPathIsReparsePoint(path); err != nil || reparse {
		if err != nil {
			return err
		}
		return errors.New("recovery stage directory became a reparse point")
	}
	return nil
}

func removeKnownRecoveryFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("recovery stage contains a replaced path")
	}
	if reparse, err := recoveryPathIsReparsePoint(path); err != nil || reparse {
		if err != nil {
			return err
		}
		return errors.New("recovery stage contains a reparse point")
	}
	return os.Remove(path)
}

func containsVersion(versions []uint64, wanted uint64) bool {
	for _, version := range versions {
		if version == wanted {
			return true
		}
	}
	return false
}

func constantTimeBindingEqual(left, right [32]byte) bool {
	return hmac.Equal(left[:], right[:])
}
