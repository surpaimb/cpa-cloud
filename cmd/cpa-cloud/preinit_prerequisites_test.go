package main

// Independently authored for docs/employee-self-cli-preinit-prerequisites-contract.md.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSelfSubscriptionCLIPrerequisitesBeforeInitWrites(t *testing.T) {
	binary := buildPreinitTestCLI(t)
	const password = "synthetic-preinit-admin-password"
	const required = " requires --employee-self-service-enabled, --employee-self-subscription-status-enabled, and --employee-self-wallet-balance-enabled"
	features := []struct {
		name     string
		flags    []string
		firstBad string
	}{
		{"purchase-snapshot", []string{"--employee-self-subscription-purchase-snapshot-enabled"}, "--employee-self-subscription-purchase-snapshot-enabled" + required},
		{"renewal", []string{"--employee-self-subscription-renewal-enabled"}, "--employee-self-subscription-renewal-enabled" + required},
		{"both", []string{"--employee-self-subscription-purchase-snapshot-enabled", "--employee-self-subscription-renewal-enabled"}, "--employee-self-subscription-purchase-snapshot-enabled" + required},
	}
	prerequisites := []string{"--employee-self-service-enabled", "--employee-self-subscription-status-enabled", "--employee-self-wallet-balance-enabled"}

	existing := filepath.Join(t.TempDir(), "initialized")
	code, stdout, stderr := runPreinitTestCLI(t, binary, []string{"--data-dir", existing, "--init"}, password+"\n")
	if code != 0 || !strings.Contains(stdout, "Initialized administrator admin.") {
		t.Fatalf("prepare initialized directory: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if err := os.WriteFile(filepath.Join(existing, "preinit-sentinel"), []byte("must remain byte-for-byte unchanged"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, feature := range features {
		for mask := 0; mask < 7; mask++ { // All incomplete subsets of the three prerequisites.
			for _, mode := range []string{"service", "init"} {
				for _, useExisting := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/prerequisites-%03b/%s/existing-%t", feature.name, mask, mode, useExisting), func(t *testing.T) {
						dataDir := filepath.Join(t.TempDir(), "must-stay-absent")
						if useExisting {
							dataDir = existing
						}
						before := map[string]string(nil)
						if useExisting {
							before = snapshotPreinitTestDirectory(t, dataDir)
						}
						listen := preinitTestFreeAddress(t)
						args := []string{"--data-dir", dataDir, "--listen", listen}
						args = append(args, feature.flags...)
						for bit, flag := range prerequisites {
							if mask&(1<<bit) != 0 {
								args = append(args, flag)
							}
						}
						if mode == "init" {
							args = append(args, "--init")
						}
						code, stdout, stderr := runPreinitTestCLI(t, binary, args, password+"\n")
						if code != 1 || !strings.Contains(stderr, feature.firstBad) || stdout != "" {
							t.Fatalf("invalid opt-in accepted or wrong error: code=%d stdout=%q stderr=%q", code, stdout, stderr)
						}
						if strings.Contains(stderr, password) {
							t.Fatal("administrator password appeared in CLI error")
						}
						if useExisting {
							if after := snapshotPreinitTestDirectory(t, dataDir); !reflect.DeepEqual(before, after) {
								t.Fatalf("rejected opt-in changed initialized directory: before=%v after=%v", before, after)
							}
						} else if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
							t.Fatalf("rejected opt-in created data directory: %v", err)
						}
						listener, err := net.Listen("tcp", listen)
						if err != nil {
							t.Fatalf("rejected opt-in left listener at %s: %v", listen, err)
						}
						listener.Close()
					})
				}
			}
		}
	}

	for _, feature := range features {
		t.Run(feature.name+"/all-prerequisites", func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "valid-init")
			args := []string{"--data-dir", dataDir, "--init"}
			args = append(args, feature.flags...)
			args = append(args, prerequisites...)
			code, stdout, stderr := runPreinitTestCLI(t, binary, args, password+"\n")
			if code != 0 || !strings.Contains(stdout, "Initialized administrator admin.") {
				t.Fatalf("valid init failed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			code, stdout, stderr = runPreinitTestCLI(t, binary, []string{"--data-dir", dataDir, "--check-initialized"}, "")
			if code != 0 || stdout != "" || stderr != "" {
				t.Fatalf("valid init not observable: code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			servePreinitTestHealth(t, binary, dataDir, append(append([]string(nil), feature.flags...), prerequisites...))
		})
	}
}

type forbiddenPreinitPasswordRead struct{}

func (forbiddenPreinitPasswordRead) Read([]byte) (int, error) {
	panic("invalid opt-in read the administrator password before validation")
}

func TestSelfSubscriptionCLIRejectsBeforePasswordRead(t *testing.T) {
	for _, feature := range []string{
		"--employee-self-subscription-purchase-snapshot-enabled",
		"--employee-self-subscription-renewal-enabled",
	} {
		t.Run(feature, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "must-stay-absent")
			code, err := runCLI([]string{"--data-dir", dataDir, "--init", feature}, forbiddenPreinitPasswordRead{}, &bytes.Buffer{})
			if code != 1 || err == nil || !strings.Contains(err.Error(), feature+" requires ") {
				t.Fatalf("invalid opt-in reached initialization: code=%d err=%v", code, err)
			}
			if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
				t.Fatalf("invalid opt-in created data directory: %v", err)
			}
		})
	}
}

func buildPreinitTestCLI(t *testing.T) string {
	t.Helper()
	name := "cpa-cloud"
	goName := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
		goName += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", goName), "build", "-trimpath", "-o", binary, ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build real CLI: %v: %s", err, output)
	}
	return binary
}

func runPreinitTestCLI(t *testing.T, binary string, args []string, input string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("real CLI did not exit: %v", ctx.Err())
	}
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), stdout.String(), stderr.String()
	}
	t.Fatalf("run real CLI: %v", err)
	return 0, "", ""
}

func snapshotPreinitTestDirectory(t *testing.T, root string) map[string]string {
	t.Helper()
	items := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			items[relative] = "directory"
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected test fixture type at %s: %s", relative, entry.Type())
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		items[relative] = fmt.Sprintf("file:%d:%x", len(content), sha256.Sum256(content))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func preinitTestFreeAddress(t *testing.T) string {
	t.Helper()
	for {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		listener.Close()
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			t.Fatal(err)
		}
		if port != "8787" {
			return address
		}
	}
}

func servePreinitTestHealth(t *testing.T, binary, dataDir string, flags []string) {
	t.Helper()
	address := preinitTestFreeAddress(t)
	args := []string{"--data-dir", dataDir, "--listen", address, "--shutdown-on-stdin-eof"}
	args = append(args, flags...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		stdin.Close()
		if !waited {
			cancel()
			command.Wait()
		}
	}()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(8 * time.Second)
	for {
		response, err := client.Get("http://" + address + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			command.Wait()
			waited = true
			t.Fatalf("valid prerequisites did not start service: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	waited = true
	if err != nil {
		t.Fatalf("valid prerequisites did not shut down cleanly: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}
