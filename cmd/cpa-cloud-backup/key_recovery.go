package main

// Independently implemented from docs/adr/0004-portable-backup-recovery-material.md.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"cpacloud.local/server/internal/recoverymaterial"
)

func runKeyExport(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	flags := flag.NewFlagSet("cpa-cloud-backup key-export", flag.ContinueOnError)
	flags.SetOutput(stdout)
	var providerStore, dataDir, providerID, versionsValue, output string
	flags.StringVar(&providerStore, "provider-store", "", "source Windows DPAPI provider store")
	flags.StringVar(&dataDir, "data-dir", "", "source CPA Cloud data directory")
	flags.StringVar(&providerID, "provider-id", "", "backup key provider identifier")
	flags.StringVar(&versionsValue, "versions", "", "strictly increasing comma-separated key versions")
	flags.StringVar(&output, "output", "", "new encrypted recovery material file")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 2, err
	}
	if flags.NArg() != 0 || providerStore == "" || dataDir == "" || providerID == "" || versionsValue == "" || output == "" {
		return 2, errors.New("key-export requires --provider-store, --data-dir, --provider-id, --versions, and --output")
	}
	versions, err := parseRecoveryVersions(versionsValue)
	if err != nil {
		return 2, err
	}
	password, err := readPassword(stdin)
	if err != nil {
		return 1, err
	}
	defer wipe(password)
	reference, err := recoverymaterial.Export(ctx, providerStore, dataDir, output, providerID, versions, password)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "Created encrypted recovery material format v%d for provider %s with %d explicit versions.\n", reference.EnvelopeVersion, reference.ProviderID, len(reference.Versions))
	return 0, nil
}

func runKeyVerify(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	flags := flag.NewFlagSet("cpa-cloud-backup key-verify", flag.ContinueOnError)
	flags.SetOutput(stdout)
	var input string
	flags.StringVar(&input, "input", "", "encrypted recovery material file")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 2, err
	}
	if flags.NArg() != 0 || input == "" {
		return 2, errors.New("key-verify requires --input")
	}
	password, err := readPassword(stdin)
	if err != nil {
		return 1, err
	}
	defer wipe(password)
	reference, err := recoverymaterial.Verify(ctx, input, password)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "Verified encrypted recovery material format v%d for provider %s with %d explicit versions.\n", reference.EnvelopeVersion, reference.ProviderID, len(reference.Versions))
	return 0, nil
}

func runKeyImport(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	flags := flag.NewFlagSet("cpa-cloud-backup key-import", flag.ContinueOnError)
	flags.SetOutput(stdout)
	var input, backupPackage, targetRoot string
	flags.StringVar(&input, "input", "", "encrypted recovery material file")
	flags.StringVar(&backupPackage, "backup", "", "automated backup v2 package")
	flags.StringVar(&targetRoot, "target-root", "", "new recovery root")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 2, err
	}
	if flags.NArg() != 0 || input == "" || backupPackage == "" || targetRoot == "" {
		return 2, errors.New("key-import requires --input, --backup, and --target-root")
	}
	password, err := readPassword(stdin)
	if err != nil {
		return 1, err
	}
	defer wipe(password)
	reference, err := recoverymaterial.Import(ctx, input, backupPackage, targetRoot, password)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "Imported recovery material format v%d for provider %s with %d versions into a new root containing data/ and provider-store/.\n", reference.EnvelopeVersion, reference.ProviderID, len(reference.Versions))
	return 0, nil
}

func parseRecoveryVersions(value string) ([]uint64, error) {
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > 512 {
		return nil, errors.New("--versions must contain between 1 and 512 explicit versions")
	}
	versions := make([]uint64, len(parts))
	for index, part := range parts {
		if part == "" || strings.TrimSpace(part) != part {
			return nil, errors.New("--versions must be a comma-separated list of decimal integers")
		}
		version, err := strconv.ParseUint(part, 10, 64)
		if err != nil || version == 0 || version > 9007199254740991 {
			return nil, errors.New("--versions contains an unsupported version")
		}
		if index > 0 && version <= versions[index-1] {
			return nil, errors.New("--versions must be strictly increasing without duplicates")
		}
		versions[index] = version
	}
	return versions, nil
}
