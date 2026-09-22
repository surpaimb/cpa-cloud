package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/service"
)

func TestCheckInitializedExitCodesAndModeExclusion(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	code, err := runCLI([]string{"--check-initialized", "--data-dir", missing}, strings.NewReader(""), io.Discard)
	if err != nil || code != 3 {
		t.Fatalf("missing data directory: code=%d err=%v", code, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("check created data directory: %v", err)
	}

	conflict := filepath.Join(t.TempDir(), "conflict")
	code, err = runCLI([]string{"--init", "--check-initialized", "--data-dir", conflict}, strings.NewReader("a-valid-admin-password\n"), io.Discard)
	if err == nil || code != 1 {
		t.Fatalf("mutually exclusive modes: code=%d err=%v", code, err)
	}
	if _, err := os.Stat(conflict); !os.IsNotExist(err) {
		t.Fatalf("mode validation wrote data: %v", err)
	}

	dataDir := t.TempDir()
	code, err = runCLI([]string{"--init", "--data-dir", dataDir}, strings.NewReader("a-valid-admin-password\n"), io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("initialize: code=%d err=%v", code, err)
	}
	code, err = runCLI([]string{"--check-initialized", "--data-dir", dataDir}, strings.NewReader(""), io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("initialized data directory: code=%d err=%v", code, err)
	}
}

func TestShutdownOnStdinEOFGracefullyStopsServer(t *testing.T) {
	dataDir := t.TempDir()
	if err := service.Initialize(context.Background(), dataDir, strings.NewReader("a-valid-admin-password\n")); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	reader, writer := io.Pipe()
	type result struct {
		code int
		err  error
	}
	finished := make(chan result, 1)
	stopped := false
	instanceID := "23959b45-a481-4f01-b82c-43ab7eab892e"
	go func() {
		code, err := runCLI([]string{"--data-dir", dataDir, "--listen", address, "--instance-id", instanceID, "--shutdown-on-stdin-eof"}, reader, io.Discard)
		finished <- result{code: code, err: err}
	}()
	defer func() {
		_ = writer.Close()
		_ = reader.Close()
		if !stopped {
			select {
			case <-finished:
			case <-time.After(10 * time.Second):
			}
		}
	}()

	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, requestErr := client.Get("http://" + address + "/healthz")
		if requestErr == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && strings.Contains(string(body), `"instance_id":"`+instanceID+`"`) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		stopped = true
		if result.err != nil || result.code != 0 {
			t.Fatalf("EOF shutdown: code=%d err=%v", result.code, result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("service did not stop after stdin EOF")
	}
	response, err := client.Get("http://" + address + "/healthz")
	if err == nil {
		response.Body.Close()
		t.Fatal("service still accepted requests after EOF shutdown")
	}
}

func TestInstanceIDValidationDoesNotWriteData(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "unused")
	code, err := runCLI([]string{"--data-dir", dataDir, "--instance-id", "not-a-uuid"}, strings.NewReader(""), io.Discard)
	if err == nil || code != 1 {
		t.Fatalf("invalid instance id: code=%d err=%v", code, err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("invalid instance id wrote data: %v", err)
	}
	if !validUUID("23959b45-a481-4f01-b82c-43ab7eab892e") || validUUID("23959b45a4814f01b82c43ab7eab892e") {
		t.Fatal("UUID validation boundaries are incorrect")
	}
}

func TestHelpDoesNotStartOrWriteData(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "unused")
	var output bytes.Buffer
	code, err := runCLI([]string{"--help", "--data-dir", dataDir}, strings.NewReader(""), &output)
	if err != nil || code != 0 {
		t.Fatalf("help: code=%d err=%v", code, err)
	}
	if !strings.Contains(output.String(), "check-initialized") || !strings.Contains(output.String(), "shutdown-on-stdin-eof") {
		t.Fatalf("help omitted launcher flags: %s", output.String())
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("help wrote data directory: %v", err)
	}
}
