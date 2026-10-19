package main

// Independently authored from docs/linux-systemd-offline-recovery-cli-contract.md.
import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"

	"cpacloud.local/server/internal/offlinerecovery"
)

func runSystemdV3(ctx context.Context, command string, args []string, out io.Writer) (int, error) {
	var options offlinerecovery.Options
	options.Restore = command == "systemd-v3-restore"
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	// The flag package includes untrusted argument values in errors/help. Never
	// forward it to stdout, stderr or the main logger for these explicit modes.
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.Input, "input", "", "")
	flags.StringVar(&options.Seed, "binding-seed", "", "")
	flags.StringVar(&options.Target, "data-dir", "", "")
	flags.StringVar(&options.Provider, "expect-provider", "", "")
	flags.StringVar(&options.Instance, "expect-instance", "", "")
	flags.StringVar(&options.MasterID, "expect-master-id", "", "")
	flags.StringVar(&options.WrappingID, "expect-wrapping-id", "", "")
	flags.StringVar(&options.SourceVersion, "expect-source-version", "", "")
	var masterVersion, wrappingVersion string
	flags.StringVar(&masterVersion, "expect-master-version", "", "")
	flags.StringVar(&wrappingVersion, "expect-wrapping-version", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2, offlinerecovery.Failure("arguments")
	}
	var err error
	options.MasterVersion, err = strconv.ParseUint(masterVersion, 10, 64)
	if err != nil || strconv.FormatUint(options.MasterVersion, 10) != masterVersion {
		return 2, offlinerecovery.Failure("arguments")
	}
	options.WrappingVersion, err = strconv.ParseUint(wrappingVersion, 10, 64)
	if err != nil || strconv.FormatUint(options.WrappingVersion, 10) != wrappingVersion || options.Validate(version) != nil {
		return 2, offlinerecovery.Failure("arguments")
	}
	if err := offlinerecovery.Run(ctx, options); err != nil {
		return 1, err
	}
	mode := "verify"
	if options.Restore {
		mode = "restore"
	}
	if _, err := fmt.Fprintln(out, "OFFLINE_V3 "+mode+" ok"); err != nil {
		return 1, offlinerecovery.Failure("cleanup")
	}
	return 0, nil
}
