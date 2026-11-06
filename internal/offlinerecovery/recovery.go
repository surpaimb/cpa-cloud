// Package offlinerecovery provides the bounded systemd v3 offline CLI operation.
// Independently authored; see docs/linux-systemd-offline-recovery-cli-contract.md.
package offlinerecovery

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

const Unit = "cpa-cloud-backup-recovery.service"
const User = "cpa-cloud-recovery"
const RuntimeDirectory = "/run/cpa-cloud-backup-recovery"

type Options struct {
	Restore                                                                      bool
	Input, Seed, Target, Provider, Instance, MasterID, WrappingID, SourceVersion string
	MasterVersion, WrappingVersion                                               uint64
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var provider = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
var instance = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var sourceVersion = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func Failure(stage string) error {
	switch stage {
	case "arguments", "platform", "unit", "seed", "identity", "installation", "wrapping", "package", "target", "restore", "cleanup", "cancelled":
		return errors.New("OFFLINE_V3 " + stage + " failed")
	default:
		return errors.New("OFFLINE_V3 restore failed")
	}
}

func (o Options) Validate(buildVersion string) error {
	pathOK := func(path string) bool {
		return filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
	}
	if !pathOK(o.Input) || !pathOK(o.Seed) || o.Input == o.Seed ||
		(o.Restore && (!pathOK(o.Target) || o.Target == o.Input || o.Target == o.Seed)) || (!o.Restore && o.Target != "") ||
		(o.Restore && (containsPath(o.Seed, o.Target) || containsPath(o.Target, o.Seed) || containsPath(o.Target, o.Input) || containsPath(RuntimeDirectory, o.Target))) ||
		!provider.MatchString(o.Provider) || !instance.MatchString(o.Instance) ||
		!identifier.MatchString(o.MasterID) || !identifier.MatchString(o.WrappingID) || o.MasterID == o.WrappingID ||
		o.MasterVersion == 0 || o.WrappingVersion == 0 || o.MasterVersion > 9007199254740991 || o.WrappingVersion > 9007199254740991 ||
		!sourceVersion.MatchString(o.SourceVersion) || o.SourceVersion != buildVersion {
		return Failure("arguments")
	}
	return nil
}

func containsPath(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
