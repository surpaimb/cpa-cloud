// Independently authored from the authorized v0.1.0 packaging contract,
// the CPA Cloud product/preview specifications, and public Go standard-library
// APIs. No reference-product code, tests, fixtures, or license text is copied.
// All file fixtures are owned synthetic temporary files; no service is started.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	rdebug "runtime/debug"
	"strings"
	"testing"
)

func releaseWrite(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The following two notice fixtures are unmodified full texts from the locked
// npm rolldown@1.2.9 package, https://registry.npmjs.org/rolldown/1.2.9.
// They are permissively licensed notice material, not copied implementation.
// LICENSE SHA256 23ecfff35a5a2e80d92142f75228912c3b1abc4b5a8337a821ff4397e2f9f734.
// THIRD-PARTY-LICENSE SHA256 a877291d800ed43692f3f9ae09d8e01cc6f7293ad39d43896059c188ffbb8b7c.
const releaseRolldownLicense = "MIT License\n\nCopyright (c) 2024-present VoidZero Inc. & Contributors\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\nof this software and associated documentation files (the \"Software\"), to deal\nin the Software without restriction, including without limitation the rights\nto use, copy, modify, merge, publish, distribute, sublicense, and/or sell\ncopies of the Software, and to permit persons to whom the Software is\nfurnished to do so, subject to the following conditions:\n\nThe above copyright notice and this permission notice shall be included in all\ncopies or substantial portions of the Software.\n\nTHE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR\nIMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,\nFITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE\nAUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER\nLIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,\nOUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE\nSOFTWARE.\n\nend of terms and conditions\n\nThe licenses of externally maintained libraries from which parts of the Software is derived are listed [here](https://github.com/rolldown/rolldown/blob/main/THIRD-PARTY-LICENSE).\n"
const releaseRolldownThirdPartyLicense = "The MIT License (MIT)\n\nCopyright (c) 2017 [these people](https://github.com/rollup/rollup/graphs/contributors)\n\nPermission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the \"Software\"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:\n\nThe above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.\n\nTHE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.\n\n---\n\nMIT License\n\nCopyright (c) 2020 Evan Wallace\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\nof this software and associated documentation files (the \"Software\"), to deal\nin the Software without restriction, including without limitation the rights\nto use, copy, modify, merge, publish, distribute, sublicense, and/or sell\ncopies of the Software, and to permit persons to whom the Software is\nfurnished to do so, subject to the following conditions:\n\nThe above copyright notice and this permission notice shall be included in all\ncopies or substantial portions of the Software.\n\nTHE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR\nIMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,\nFITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE\nAUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER\nLIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,\nOUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE\nSOFTWARE."

func releaseLockedFrontendFixture(t *testing.T, root string, includeRolldown bool) {
	t.Helper()
	lock, err := os.ReadFile(filepath.Join(releaseTestRoot(t), "web", "bun.lock"))
	if err != nil {
		t.Fatal(err)
	}
	releaseWrite(t, filepath.Join(root, "web", "bun.lock"), string(lock))
	releaseWrite(t, filepath.Join(root, "web", "dist", "index.html"), "<h1>synthetic static fixture, not the real Web bundle</h1>")
	for _, d := range frontendRuntimeDependencies {
		if d.Name == "rolldown" {
			continue
		}
		base := filepath.Join(root, "web", "node_modules", d.Name)
		releaseWrite(t, filepath.Join(base, "package.json"), fmt.Sprintf("{\"name\":%q,\"version\":%q,\"license\":%q}", d.Name, d.Version, d.License))
		releaseWrite(t, filepath.Join(base, d.NoticeFile), "SYNTHETIC FULL NOTICE "+d.Name+"\nEND NOTICE\n")
	}
	if includeRolldown {
		base := filepath.Join(root, "web", "node_modules", "rolldown")
		releaseWrite(t, filepath.Join(base, "package.json"), "{\"name\":\"rolldown\",\"version\":\"1.2.9\",\"license\":\"MIT\"}")
		releaseWrite(t, filepath.Join(base, "LICENSE"), releaseRolldownLicense)
		releaseWrite(t, filepath.Join(base, "THIRD-PARTY-LICENSE"), releaseRolldownThirdPartyLicense)
	}
}

func TestReleaseRolldownInputs(t *testing.T) {
	for _, name := range []string{"complete", "missing_package", "wrong_version", "wrong_license", "missing_LICENSE", "missing_THIRD_PARTY_LICENSE", "truncated_LICENSE", "truncated_THIRD_PARTY_LICENSE", "missing_lock", "changed_lock"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			releaseLockedFrontendFixture(t, root, name != "missing_package")
			base := filepath.Join(root, "web", "node_modules", "rolldown")
			switch name {
			case "wrong_version":
				releaseWrite(t, filepath.Join(base, "package.json"), "{\"name\":\"rolldown\",\"version\":\"1.2.10\",\"license\":\"MIT\"}")
			case "wrong_license":
				releaseWrite(t, filepath.Join(base, "package.json"), "{\"name\":\"rolldown\",\"version\":\"1.2.9\",\"license\":\"UNKNOWN\"}")
			case "missing_LICENSE":
				if err := os.Remove(filepath.Join(base, "LICENSE")); err != nil {
					t.Fatal(err)
				}
			case "missing_THIRD_PARTY_LICENSE":
				if err := os.Remove(filepath.Join(base, "THIRD-PARTY-LICENSE")); err != nil {
					t.Fatal(err)
				}
			case "truncated_LICENSE":
				releaseWrite(t, filepath.Join(base, "LICENSE"), "MIT\n")
			case "truncated_THIRD_PARTY_LICENSE":
				releaseWrite(t, filepath.Join(base, "THIRD-PARTY-LICENSE"), "MIT\n")
			case "missing_lock":
				if err := os.Remove(filepath.Join(root, "web", "bun.lock")); err != nil {
					t.Fatal(err)
				}
			case "changed_lock":
				releaseWrite(t, filepath.Join(root, "web", "bun.lock"), "{}\n")
			}
			err := collectWebAssets(root, filepath.Join(t.TempDir(), "assets"))
			if name == "complete" {
				if err != nil {
					t.Fatalf("LICENSE_COMPLETE_FIXTURE_REJECTED: %v", err)
				}
			} else if err == nil {
				t.Fatal("LICENSE_OLD_ACCEPTED_ROLLDOWN_INPUT")
			}
		})
	}
}

func TestReleaseRolldownAssetValidation(t *testing.T) {
	root := t.TempDir()
	releaseLockedFrontendFixture(t, root, true)
	assets := filepath.Join(t.TempDir(), "assets")
	if err := collectWebAssets(root, assets); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wrong_version", "wrong_license", "missing_LICENSE", "missing_THIRD_PARTY_LICENSE", "truncated_LICENSE", "truncated_THIRD_PARTY_LICENSE", "wrong_lock_binding", "duplicate_manifest"} {
		t.Run(name, func(t *testing.T) {
			copy := t.TempDir()
			if err := copyTree(assets, copy); err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(copy, "THIRD-PARTY-LICENSES", "npm", "rolldown_1.2.9")
			switch name {
			case "wrong_version":
				releaseWrite(t, filepath.Join(base, "package.json"), "{\"name\":\"rolldown\",\"version\":\"1.2.10\",\"license\":\"MIT\"}")
			case "wrong_license":
				releaseWrite(t, filepath.Join(base, "package.json"), "{\"name\":\"rolldown\",\"version\":\"1.2.9\",\"license\":\"UNKNOWN\"}")
			case "missing_LICENSE":
				if err := os.Remove(filepath.Join(base, "LICENSE")); err != nil {
					t.Fatal(err)
				}
			case "missing_THIRD_PARTY_LICENSE":
				if err := os.Remove(filepath.Join(base, "THIRD-PARTY-LICENSE")); err != nil {
					t.Fatal(err)
				}
			case "truncated_LICENSE":
				releaseWrite(t, filepath.Join(base, "LICENSE"), "MIT\n")
			case "truncated_THIRD_PARTY_LICENSE":
				releaseWrite(t, filepath.Join(base, "THIRD-PARTY-LICENSE"), "MIT\n")
			}
			var manifest webAssetManifest
			if err := readJSON(filepath.Join(copy, "WEB-ASSETS.json"), &manifest); err != nil {
				t.Fatal(err)
			}
			files, err := inventoryFiles(copy, map[string]bool{"WEB-ASSETS.json": true})
			if err != nil {
				t.Fatal(err)
			}
			manifest.Files = files
			if name == "wrong_lock_binding" {
				manifest.SourceLockSHA256 = strings.Repeat("0", 64)
			}
			if name == "duplicate_manifest" {
				manifest.Files = append(manifest.Files, manifest.Files[0])
			}
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			releaseWrite(t, filepath.Join(copy, "WEB-ASSETS.json"), string(data))
			if validateWebAssets(copy) == nil {
				t.Fatal("LICENSE_REHASHED_INVALID_ASSET_ACCEPTED")
			}
		})
	}
}

func TestReleaseFrontendLockLineEndings(t *testing.T) {
	root := t.TempDir()
	releaseLockedFrontendFixture(t, root, true)
	path := filepath.Join(root, "web", "bun.lock")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lf := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, content := range []string{lf, strings.ReplaceAll(lf, "\n", "\r\n")} {
		releaseWrite(t, path, content)
		if err := collectWebAssets(root, filepath.Join(t.TempDir(), "assets")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReleaseEntryKinds(t *testing.T) {
	for _, mode := range []fs.FileMode{os.ModeSymlink, os.ModeNamedPipe, os.ModeSocket, os.ModeDevice, os.ModeCharDevice, os.ModeIrregular} {
		if validateEntryMode(mode) == nil {
			t.Fatal("PACKAGE_NONREGULAR_ENTRY_ACCEPTED")
		}
	}
	for _, mode := range []fs.FileMode{0o644, 0o755, os.ModeDir | 0o755} {
		if err := validateEntryMode(mode); err != nil {
			t.Fatal(err)
		}
	}
}

func releaseTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(root) == "scripts" {
		root = filepath.Dir(root)
	}
	return root
}

func TestReleasePackagingDryRun(t *testing.T) {
	root := releaseTestRoot(t)
	sha := gitCommit(root)
	input := t.TempDir()
	releaseWrite(t, filepath.Join(input, "web", "dist", "index.html"), "<h1>synthetic packaging dry-run</h1>")
	releaseWrite(t, filepath.Join(input, "web", "dist", "assets", "synthetic.js"), "/* no real Web application */")
	releaseLockedFrontendFixture(t, input, true)
	assets := filepath.Join(t.TempDir(), "assets")
	if err := collectWebAssets(input, assets); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	options := releaseOptions()
	options.source, options.sourceSHA, options.webAssets, options.output = root, sha, assets, output
	t.Setenv("GITHUB_SHA", strings.Repeat("f", 40))
	if err := buildPackage(options); err != nil {
		t.Fatalf("PACKAGE_DRY_RUN: %v", err)
	}
	name := "cpa-cloud_v0.1.0_linux_amd64"
	archive := filepath.Join(output, name+".tar.gz")
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err := os.ReadFile(archive + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	if string(sidecar) != fmt.Sprintf("%x  %s.tar.gz\n", sha256.Sum256(raw), name) {
		t.Fatal("PACKAGE_ARCHIVE_SHA")
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	unpacked := t.TempDir()
	seen := map[string]bool{}
	files := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == name {
			if header.Typeflag != tar.TypeDir {
				t.Fatal("PACKAGE_ARCHIVE_ROOT")
			}
			continue
		}
		if !strings.HasPrefix(header.Name, name+"/") {
			t.Fatal("PACKAGE_ARCHIVE_ESCAPE")
		}
		relative := strings.TrimPrefix(header.Name, name+"/")
		if relative == "" || strings.Contains(relative, "..") || strings.Contains(relative, "\\") || strings.Contains(relative, ":") {
			t.Fatal("PACKAGE_ARCHIVE_BAD_PATH")
		}
		key := strings.ToLower(relative)
		if seen[key] {
			t.Fatal("PACKAGE_ARCHIVE_DUPLICATE")
		}
		seen[key] = true
		destination := filepath.Join(unpacked, filepath.FromSlash(relative))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, 0o755); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(body)) != header.Size {
				t.Fatal("PACKAGE_ARCHIVE_TRUNCATED")
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, body, 0o644); err != nil {
				t.Fatal(err)
			}
			if relative == "cpa-cloud" || relative == "cpa-cloud-backup" {
				if header.Mode != 0o755 {
					t.Fatal("PACKAGE_LINUX_EXECUTABLE_MODE")
				}
				if err := verifyELFVersion(destination, "v0.1.0"); err != nil {
					t.Fatal(err)
				}
				if verifyELFVersion(destination, "v0.1.1") == nil {
					t.Fatal("PACKAGE_WRONG_EMBEDDED_VERSION_ACCEPTED")
				}
			}
			files++
		default:
			t.Fatal("PACKAGE_ARCHIVE_NONREGULAR")
		}
	}
	if files < 20 || !seen["cpa-cloud"] || !seen["cpa-cloud-backup"] || !seen["web/index.html"] || !seen["version"] {
		t.Fatal("PACKAGE_REQUIRED_FILES")
	}
	if err := verifyTreeChecksums(unpacked); err != nil {
		t.Fatal(err)
	}
	var manifest releaseManifest
	if err := readJSON(filepath.Join(unpacked, "MANIFEST.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SourceSHA != sha || manifest.Version != "v0.1.0" || manifest.Target != "linux/amd64" || len(manifest.Binaries) != 2 || len(manifest.Files)+2 != files {
		t.Fatal("PACKAGE_MANIFEST_IDENTITY")
	}
	for _, b := range manifest.Binaries {
		data, err := os.ReadFile(filepath.Join(unpacked, b.Name))
		if err != nil {
			t.Fatal(err)
		}
		if b.Version != "v0.1.0" || b.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
			t.Fatal("PACKAGE_BINARY_BINDING")
		}
	}
	if len(manifest.GoModules) == 0 {
		t.Fatal("PACKAGE_EMPTY_LINKED_UNION")
	}
	cacheCommand := exec.Command("go", "env", "GOMODCACHE")
	cacheCommand.Dir, cacheCommand.Env = root, buildEnvironment(options)
	cacheBytes, err := cacheCommand.Output()
	if err != nil {
		t.Fatal("PACKAGE_INDEPENDENT_CACHE_QUERY_ERROR")
	}
	for _, m := range manifest.GoModules {
		if len(m.Binaries) == 0 || len(m.Notices) == 0 {
			t.Fatal("PACKAGE_LINK_SET_MISSING")
		}
		dir := filepath.Join(strings.TrimSpace(string(cacheBytes)), filepath.FromSlash(m.Path+"@"+m.Version))
		for _, notice := range m.Notices {
			original, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(notice)))
			if err != nil {
				t.Fatal(err)
			}
			copied, err := os.ReadFile(filepath.Join(unpacked, "THIRD-PARTY-LICENSES", "go-modules", safeName(m.Path+"@"+m.Version), filepath.FromSlash(notice)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, copied) {
				t.Fatal("PACKAGE_GO_NOTICE_NOT_FULL_TEXT")
			}
		}
	}
	if buildPackage(options) == nil {
		t.Fatal("PACKAGE_ARCHIVE_OVERWRITE_ACCEPTED")
	}
	unchanged, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(raw, unchanged) {
		t.Fatal("PACKAGE_ARCHIVE_OVERWRITE")
	}
	t.Logf("PACKAGE_DRY_RUN_OK target=linux/amd64 binaries=2 files=%d modules=%d source=%s synthetic_web=true published=false", files, len(manifest.GoModules), sha)
}

func TestReleaseTags(t *testing.T) {
	for _, tag := range []string{"v0.1.0-preview.1", "v12.34.56-preview.789", "v0.1.0"} {
		t.Run("accept_"+tag, func(t *testing.T) {
			if err := run([]string{"validate-tag", "--tag", tag}); err != nil {
				t.Fatalf("PACKAGE_TAG_REJECTED: %v", err)
			}
		})
	}
	for _, tag := range []string{"", "v0.1.1", "v1.0.0", "v0.1.0-rc.1", "v0.1.0\n", "../v0.1.0", "v0.1.0-preview.x"} {
		t.Run("reject_"+strings.ReplaceAll(tag, "/", "_"), func(t *testing.T) {
			if run([]string{"validate-tag", "--tag", tag}) == nil {
				t.Fatal("PACKAGE_BAD_TAG_ACCEPTED")
			}
		})
	}
	if validatePreviewTag("v0.1.0") == nil {
		t.Fatal("PACKAGE_PREVIEW_VALIDATOR_WIDENED")
	}
}

func TestReleaseArgumentAmbiguity(t *testing.T) {
	for _, args := range [][]string{
		{"validate-tag", "--tag", "v0.1.0-preview.1", "extra"},
		{"validate-tag", "--tag", "v0.1.0-preview.1", "--tag", "v0.1.0-preview.2"},
	} {
		if run(args) == nil {
			t.Fatal("PACKAGE_AMBIGUOUS_ARGUMENTS_ACCEPTED")
		}
	}
}

func TestReleaseLibcNotices(t *testing.T) {
	want := []string{"LICENSE", "LICENSE-3RD-PARTY.md"}
	if !reflect.DeepEqual(goNoticeFiles["modernc.org/libc"], want) {
		t.Fatal("PACKAGE_LIBC_NOTICE_LAYOUT_STALE")
	}
}

func TestReleaseChecksumsIncludeNestedNames(t *testing.T) {
	root := t.TempDir()
	releaseWrite(t, filepath.Join(root, "web", "SHA256SUMS.txt"), "ordinary nested asset\n")
	releaseWrite(t, filepath.Join(root, "web", "index.html"), "<h1>synthetic</h1>\n")
	if err := writeTreeChecksums(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "SHA256SUMS.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(strings.TrimSpace(string(b)), "\n")) != 2 ||
		!strings.Contains(string(b), "  web/SHA256SUMS.txt\n") {
		t.Fatal("PACKAGE_FILE_CLOSURE_INCOMPLETE")
	}
}

func TestReleaseCopyBoundaries(t *testing.T) {
	t.Run("existing_destination", func(t *testing.T) {
		root := t.TempDir()
		source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
		releaseWrite(t, source, "new")
		releaseWrite(t, destination, "retained")
		if copyFile(source, destination, 0o644) == nil {
			t.Fatal("PACKAGE_OVERWRITE")
		}
		b, _ := os.ReadFile(destination)
		if string(b) != "retained" {
			t.Fatal("PACKAGE_DESTINATION_CHANGED")
		}
	})
	t.Run("directory_input_has_no_output", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "output")
		if copyFile(root, target, 0o644) == nil {
			t.Fatal("PACKAGE_NONREGULAR_ACCEPTED")
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatal("PACKAGE_NONREGULAR_LEFT_OUTPUT")
		}
	})
}

func TestReleaseInvalidPackageNoOutput(t *testing.T) {
	for _, extra := range [][]string{
		{"--source-sha", "unknown", "--include-backup-cli"},
		{"--source-sha", strings.Repeat("a", 40)},
		{"--source-sha", strings.Repeat("a", 40), "--include-backup-cli", "--target-os", "windows", "--asset-os", "windows"},
	} {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		args := []string{"package", "--version", "v0.1.0", "--source", root, "--web-assets", filepath.Join(root, "web-input"), "--output", output, "--target-os", "linux", "--target-arch", "amd64", "--asset-os", "linux"}
		if run(append(args, extra...)) == nil {
			t.Fatal("PACKAGE_INVALID_OPTIONS_ACCEPTED")
		}
		if _, err := os.Lstat(output); !os.IsNotExist(err) {
			t.Fatal("PACKAGE_INVALID_OPTIONS_WROTE_OUTPUT")
		}
	}
}

func releaseOptions() packageOptions {
	return packageOptions{version: "v0.1.0", sourceSHA: strings.Repeat("a", 40),
		targetOS: "linux", targetArch: "amd64", assetOS: "linux", includeBackupCLI: true}
}

func TestReleaseStableTargetPolicy(t *testing.T) {
	base := releaseOptions()
	if err := validatePackageOptions(base); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*packageOptions){
		func(o *packageOptions) { o.includeBackupCLI = false },
		func(o *packageOptions) { o.sourceSHA = "" },
		func(o *packageOptions) { o.sourceSHA = strings.Repeat("g", 40) },
		func(o *packageOptions) { o.sourceSHA += "\n" },
		func(o *packageOptions) { o.targetArch = "arm64" },
		func(o *packageOptions) { o.targetOS, o.assetOS = "windows", "windows" },
		func(o *packageOptions) { o.assetOS = "../linux" },
		func(o *packageOptions) { o.version = "v0.1.1" },
	} {
		o := base
		mutate(&o)
		if validatePackageOptions(o) == nil {
			t.Fatal("PACKAGE_INVALID_STABLE_POLICY")
		}
	}
}

func TestReleasePathPolicy(t *testing.T) {
	for _, bad := range []string{"", ".", "../x", "/x", "a/../x", "a//x", "a\\x", "C:/x", "x\nz", "a/./x"} {
		if validateRelative(bad) == nil {
			t.Fatalf("PACKAGE_PATH_ACCEPTED: %q", bad)
		}
	}
	for _, good := range []string{"web/index.html", "docs/a.md", "THIRD-PARTY-LICENSES/go-modules/a/LICENSE"} {
		if err := validateRelative(good); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{"web/node_modules/x.js", "data/state.db", "web/master.key", "web/.env", "web/config.json", "web/sessions/a"} {
		if validatePayloadPath(bad) == nil {
			t.Fatalf("PACKAGE_PRIVATE_MATERIAL_ACCEPTED: %q", bad)
		}
	}
}

func TestReleaseModuleUnionAndRejection(t *testing.T) {
	options := releaseOptions()
	info := func(name string, deps ...*rdebug.Module) binaryBuildInfo {
		return binaryBuildInfo{Name: name, Info: &rdebug.BuildInfo{
			GoVersion: "go1.26.9", Path: "cpacloud.local/server/cmd/" + name,
			Main: rdebug.Module{Path: "cpacloud.local/server", Version: "(devel)"},
			Deps: deps, Settings: []rdebug.BuildSetting{
				{Key: "GOOS", Value: "linux"}, {Key: "GOARCH", Value: "amd64"},
				{Key: "CGO_ENABLED", Value: "0"}, {Key: "-ldflags", Value: "-X main.version=v0.1.0"},
			},
		}}
	}
	dep := func() *rdebug.Module { return &rdebug.Module{Path: "modernc.org/libc", Version: "v1.70.0"} }
	binaries := []binaryBuildInfo{info("cpa-cloud", dep()), info("cpa-cloud-backup", dep())}
	got, err := reviewBuildInfos(binaries, options)
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].Binaries, []string{"cpa-cloud", "cpa-cloud-backup"}) {
		t.Fatalf("PACKAGE_UNION: %v", err)
	}
	if !reflect.DeepEqual(got[0].Notices, []string{"LICENSE", "LICENSE-3RD-PARTY.md"}) {
		t.Fatal("PACKAGE_UNION_NOTICE")
	}
	for _, name := range []string{"unknown_module", "unknown_version", "replace", "duplicate_dep", "duplicate_binary", "missing_backup", "wrong_target", "wrong_version", "wrong_package", "wrong_toolchain", "duplicate_setting", "main_replace"} {
		t.Run(name, func(t *testing.T) {
			input := []binaryBuildInfo{info("cpa-cloud", dep()), info("cpa-cloud-backup", dep())}
			d := input[0].Info.Deps[0]
			switch name {
			case "unknown_module":
				d.Path = "example.invalid/unreviewed"
			case "unknown_version":
				d.Version = "v1.70.1"
			case "replace":
				d.Replace = &rdebug.Module{Path: "../local"}
			case "duplicate_dep":
				input[0].Info.Deps = append(input[0].Info.Deps, dep())
			case "duplicate_binary":
				input[1] = input[0]
			case "missing_backup":
				input = input[:1]
			case "wrong_target":
				input[0].Info.Settings[0].Value = "windows"
			case "wrong_version":
				input[0].Info.Settings[3].Value = "-X main.version=dev"
			case "wrong_package":
				input[0].Info.Path = "other/main"
			case "wrong_toolchain":
				input[0].Info.GoVersion = "go1.26.10"
			case "duplicate_setting":
				input[0].Info.Settings = append(input[0].Info.Settings, input[0].Info.Settings[0])
			case "main_replace":
				input[0].Info.Main.Replace = &rdebug.Module{Path: "../local"}
			}
			if _, err := reviewBuildInfos(input, options); err == nil {
				t.Fatal("PACKAGE_UNREVIEWED_BUILD_ACCEPTED")
			}
		})
	}
}

func TestReleaseManifestAndChecksumClosure(t *testing.T) {
	root := t.TempDir()
	releaseWrite(t, filepath.Join(root, "web", "index.html"), "synthetic")
	releaseWrite(t, filepath.Join(root, "web", "SHA256SUMS.txt"), "nested")
	if err := writeReleaseManifest(root, releaseOptions(), strings.Repeat("a", 40), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeTreeChecksums(root); err != nil {
		t.Fatal(err)
	}
	if err := verifyTreeChecksums(root); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []string{"extra", "changed", "missing", "duplicate", "escape"} {
		t.Run(mutate, func(t *testing.T) {
			stage := t.TempDir()
			if err := copyTree(root, stage); err != nil {
				t.Fatal(err)
			}
			switch mutate {
			case "extra":
				releaseWrite(t, filepath.Join(stage, "extra.txt"), "extra")
			case "changed":
				releaseWrite(t, filepath.Join(stage, "web", "index.html"), "changed")
			case "missing":
				if err := os.Remove(filepath.Join(stage, "web", "index.html")); err != nil {
					t.Fatal(err)
				}
			case "duplicate", "escape":
				b, err := os.ReadFile(filepath.Join(stage, "SHA256SUMS.txt"))
				if err != nil {
					t.Fatal(err)
				}
				if mutate == "duplicate" {
					b = append(b, b...)
				} else {
					b = []byte(strings.Repeat("a", 64) + "  ../outside\n")
				}
				releaseWrite(t, filepath.Join(stage, "SHA256SUMS.txt"), string(b))
			}
			if verifyTreeChecksums(stage) == nil {
				t.Fatal("PACKAGE_BAD_CLOSURE_ACCEPTED")
			}
		})
	}
}

func TestReleaseFrontendClosure(t *testing.T) {
	root, assets := t.TempDir(), filepath.Join(t.TempDir(), "assets")
	releaseWrite(t, filepath.Join(root, "web", "dist", "index.html"), "<h1>synthetic only</h1>")
	releaseWrite(t, filepath.Join(root, "web", "dist", "assets", "app.js"), "/* synthetic, not a real Web build */")
	releaseLockedFrontendFixture(t, root, true)
	if err := collectWebAssets(root, assets); err != nil {
		t.Fatal(err)
	}
	if err := validateWebAssets(assets); err != nil {
		t.Fatal(err)
	}
	for _, d := range frontendRuntimeDependencies {
		for _, notice := range frontendNoticeFiles(d) {
			want := "SYNTHETIC FULL NOTICE " + d.Name + "\nEND NOTICE\n"
			if d.Name == "rolldown" {
				want = releaseRolldownLicense
				if notice == "THIRD-PARTY-LICENSE" {
					want = releaseRolldownThirdPartyLicense
				}
			}
			got, err := os.ReadFile(filepath.Join(assets, "THIRD-PARTY-LICENSES", "npm", safeName(d.Name+"@"+d.Version), notice))
			if err != nil || string(got) != want {
				t.Fatal("PACKAGE_NOTICE_TRUNCATED")
			}
		}
	}
	t.Run("tampered_notice", func(t *testing.T) {
		copy := t.TempDir()
		if err := copyTree(assets, copy); err != nil {
			t.Fatal(err)
		}
		releaseWrite(t, filepath.Join(copy, "THIRD-PARTY-LICENSES", "npm", "react_19.3.0", "LICENSE"), "altered")
		if validateWebAssets(copy) == nil {
			t.Fatal("PACKAGE_NOTICE_TAMPER_ACCEPTED")
		}
	})
	t.Run("extra_material", func(t *testing.T) {
		copy := t.TempDir()
		if err := copyTree(assets, copy); err != nil {
			t.Fatal(err)
		}
		releaseWrite(t, filepath.Join(copy, "web", "node_modules", "bad.js"), "bad")
		if validateWebAssets(copy) == nil {
			t.Fatal("PACKAGE_EXTRA_ASSET_ACCEPTED")
		}
	})
	t.Run("missing_notice", func(t *testing.T) {
		copy := t.TempDir()
		if err := copyTree(assets, copy); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(copy, "THIRD-PARTY-LICENSES", "npm", "vite_8.3.0", "LICENSE.md")); err != nil {
			t.Fatal(err)
		}
		if validateWebAssets(copy) == nil {
			t.Fatal("PACKAGE_MISSING_NOTICE_ACCEPTED")
		}
	})
}

// P2 counterexamples use owned, isolated Git repositories, never project commits.
func releaseFixtureGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatal("PACKAGE_P2_FIXTURE_GIT_ERROR")
	}
	return strings.TrimSpace(string(out))
}

func releaseSourceFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(t.TempDir(), "empty-git-config")
	releaseWrite(t, config, "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	releaseFixtureGit(t, root, "init", "-q")
	releaseFixtureGit(t, root, "config", "core.autocrlf", "false")
	releaseFixtureGit(t, root, "config", "core.hooksPath", filepath.Join(root, "no-hooks"))
	releaseFixtureGit(t, root, "config", "user.name", "Synthetic package fixture")
	releaseFixtureGit(t, root, "config", "user.email", "fixture@example.invalid")
	releaseWrite(t, filepath.Join(root, "go.mod"), "module cpacloud.local/server\n\ngo 1.26\n")
	releaseWrite(t, filepath.Join(root, "go.sum"), "")
	main := "package main\nimport _ \"cpacloud.local/server/internal/fixture\"\nfunc main() {}\n"
	releaseWrite(t, filepath.Join(root, "cmd", "cpa-cloud", "main.go"), main)
	releaseWrite(t, filepath.Join(root, "cmd", "cpa-cloud-backup", "main.go"), main)
	releaseWrite(t, filepath.Join(root, "internal", "fixture", "source.go"), "package fixture\nconst Value = 1\n")
	releaseWrite(t, filepath.Join(root, "internal", "fixture", "embed.go"),
		"package fixture\nimport \"embed\"\n//go:embed *.bin\nvar Assets embed.FS\n")
	releaseWrite(t, filepath.Join(root, "internal", "fixture", "fixture.bin"), "public tracked embed input\n")
	releaseWrite(t, filepath.Join(root, ".gitignore"), "*.ignored.go\n*.ignored.bin\n")
	releaseFixtureGit(t, root, "add", "--all")
	releaseFixtureGit(t, root, "-c", "commit.gpgSign=false", "commit", "-qm", "Synthetic source fixture")
	return root, releaseFixtureGit(t, root, "rev-parse", "HEAD")
}

func TestReleaseCRLFDiagnostic(t *testing.T) {
	rejected := false
	for _, variant := range []string{"manual_crlf", "git_checkout_crlf"} {
		t.Run(variant, func(t *testing.T) {
			root, head := releaseSourceFixture(t)
			rawGit := func(args ...string) []byte {
				command := exec.Command("git", args...)
				command.Dir = root
				out, err := command.Output()
				if err != nil {
					t.Fatal("PACKAGE_CRLF_DIAGNOSTIC_GIT_ERROR")
				}
				return out
			}
			log := func(phase string, facts map[string]any) {
				facts["phase"], facts["variant"] = phase, variant
				data, err := json.Marshal(facts)
				if err != nil {
					t.Fatal("PACKAGE_CRLF_DIAGNOSTIC_JSON_ERROR")
				}
				t.Logf("PACKAGE_CRLF_DIAGNOSTIC %s", data)
			}
			releaseFixtureGit(t, root, "config", "core.autocrlf", "true")
			names := bytes.Split(rawGit("ls-files", "-z"), []byte{0})
			for _, name := range names {
				if len(name) == 0 {
					continue
				}
				path := filepath.Join(root, filepath.FromSlash(string(name)))
				if variant == "git_checkout_crlf" {
					if err := os.Remove(path); err != nil {
						t.Fatal("PACKAGE_CRLF_DIAGNOSTIC_IO_ERROR")
					}
				} else {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal("PACKAGE_CRLF_DIAGNOSTIC_IO_ERROR")
					}
					releaseWrite(t, path, strings.ReplaceAll(string(data), "\n", "\r\n"))
				}
			}
			if variant == "git_checkout_crlf" {
				rawGit("checkout", "--force", "HEAD", "--", ".")
			}
			files := map[string]any{}
			for _, rawName := range names {
				name := string(rawName)
				if name == "" {
					continue
				}
				data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
				if err != nil {
					t.Fatal("PACKAGE_CRLF_DIAGNOSTIC_IO_ERROR")
				}
				blob := rawGit("cat-file", "blob", head+":"+name)
				lf := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
				files[name] = map[string]any{"bytes": len(data), "crlf": bytes.Count(data, []byte("\r\n")),
					"rawSHA256":      fmt.Sprintf("%x", sha256.Sum256(data)),
					"lfSHA256":       fmt.Sprintf("%x", sha256.Sum256(lf)),
					"headBlobSHA256": fmt.Sprintf("%x", sha256.Sum256(blob)),
					"rawEqualsHEAD":  bytes.Equal(data, blob), "lfEqualsHEAD": bytes.Equal(lf, blob)}
			}
			log("before-first-verify", map[string]any{"head": head, "files": files,
				"gitVersion": string(rawGit("--version")),
				"config":     string(rawGit("config", "--list", "--show-origin", "--show-scope")),
				"attributes": string(rawGit("check-attr", "--all", "--", "go.mod", "go.sum",
					"cmd/cpa-cloud/main.go", "cmd/cpa-cloud-backup/main.go", "internal/fixture/source.go")),
				"eol":   string(rawGit("ls-files", "--eol")),
				"index": string(rawGit("ls-files", "--stage")),
				"flags": string(rawGit("ls-files", "-v"))})
			actual, err := verifySource(root, head, true)
			message := ""
			if err != nil {
				message, rejected = err.Error(), true
			}
			log("first-verify", map[string]any{"actual": actual, "error": message, "head": head})
			log("status-after-first-verify", map[string]any{"status": string(rawGit("status",
				"--porcelain=v1", "--untracked-files=all", "--", "go.mod", "go.sum", "cmd", "internal"))})
		})
	}
	if rejected {
		t.Log("PACKAGE_CRLF_DIAGNOSTIC_OBSERVATION_ONLY: a first admission was rejected; no root-cause inference")
	}
}

func TestReleaseSourceP2(t *testing.T) {
	for _, variant := range []string{"clean", "crlf_transport", "ordinary_change", "staged_change", "ignored_go", "ignored_embed",
		"assume_cmd", "skip_internal", "assume_mod", "skip_sum", "active_filter_attribute"} {
		t.Run(variant, func(t *testing.T) {
			root, head := releaseSourceFixture(t)
			switch variant {
			case "crlf_transport":
				releaseFixtureGit(t, root, "config", "core.autocrlf", "true")
				for _, name := range strings.Split(releaseFixtureGit(t, root, "ls-files"), "\n") {
					path := filepath.Join(root, filepath.FromSlash(name))
					if err := os.Remove(path); err != nil {
						t.Fatal("PACKAGE_P2_FIXTURE_REMOVE_ERROR")
					}
				}
				releaseFixtureGit(t, root, "checkout", "--force", "HEAD", "--", ".")
			case "ordinary_change", "staged_change":
				releaseWrite(t, filepath.Join(root, "internal", "fixture", "source.go"), "package fixture\nconst Value = 2\n")
				if variant == "staged_change" {
					releaseFixtureGit(t, root, "add", "--", "internal/fixture/source.go")
				}
			case "ignored_go":
				releaseWrite(t, filepath.Join(root, "internal", "fixture", "extra.ignored.go"), "package fixture\nconst Hidden = 1\n")
			case "ignored_embed":
				releaseWrite(t, filepath.Join(root, "internal", "fixture", "extra.ignored.bin"), "synthetic ignored embed input")
				// Both fixture mains import this package. The real Go resolver must
				// select the ignored file through its committed go:embed glob.
				command := exec.Command("go", "list", "-json", "./internal/fixture")
				command.Dir = root
				out, err := command.Output()
				var resolved struct{ EmbedFiles []string }
				if err != nil || json.Unmarshal(out, &resolved) != nil {
					t.Fatal("PACKAGE_P2_EMBED_RESOLVER_ERROR")
				}
				if !strings.Contains(strings.Join(resolved.EmbedFiles, "\n"), "extra.ignored.bin") {
					t.Fatal("PACKAGE_P2_EMBED_NOT_SELECTED")
				}
			case "assume_cmd", "skip_internal", "assume_mod", "skip_sum":
				name, flag := "cmd/cpa-cloud/main.go", "--assume-unchanged"
				switch variant {
				case "skip_internal":
					name, flag = "internal/fixture/source.go", "--skip-worktree"
				case "assume_mod":
					name = "go.mod"
				case "skip_sum":
					name, flag = "go.sum", "--skip-worktree"
				}
				releaseFixtureGit(t, root, "update-index", flag, "--", name)
				releaseWrite(t, filepath.Join(root, filepath.FromSlash(name)), "synthetic hidden change\n")
			case "active_filter_attribute":
				// Undefined optional filter: no real content conversion is claimed.
				releaseWrite(t, filepath.Join(root, ".git", "info", "attributes"), "*.go filter=unreviewed\n")
			}
			actual, err := verifySource(root, head, true)
			if variant == "clean" || variant == "crlf_transport" {
				if err != nil || actual != head {
					t.Fatalf("PACKAGE_P2_CONTROL_REJECTED actual=%s head=%s error=%v", actual, head, err)
				}
			} else if err == nil {
				t.Fatal("PACKAGE_P2_OLD_SOURCE_ACCEPTED")
			}
		})
	}
}

func releaseInputFacts(t *testing.T, root string) map[string]string {
	t.Helper()
	facts := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			facts[filepath.ToSlash(rel)] = "directory"
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			facts[filepath.ToSlash(rel)] = fmt.Sprintf("%x", sha256.Sum256(data))
		}
		return nil
	})
	if err != nil {
		t.Fatal("PACKAGE_P2_FIXTURE_FACTS_ERROR")
	}
	return facts
}

func TestReleaseWebValidControl(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(fmt.Sprintf("preview_%t", preview), func(t *testing.T) {
			root := t.TempDir()
			releaseLockedFrontendFixture(t, root, true)
			out := filepath.Join(t.TempDir(), "assets")
			if preview {
				out = filepath.Join(root, "release-web")
			}
			if err := collectWebAssets(root, out); err != nil {
				t.Fatalf("PACKAGE_P2_VALID_WEB_CONTROL_REJECTED: %v", err)
			}
			if err := validateWebAssets(out); err != nil {
				t.Fatal("PACKAGE_P2_VALID_WEB_CONTROL_INVALID")
			}
		})
	}
}

func TestReleaseWebOutputP2(t *testing.T) {
	// Nested dist recursion is never run against the old collector. These
	// independent nonrecursive overlaps use otherwise valid Web inputs.
	for _, name := range []string{"web/node_modules/rolldown/zz-collected", "web/src/zz-collected", "cmd/zz-collected"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			releaseLockedFrontendFixture(t, root, true)
			for _, dir := range []string{"web/src", "cmd"} {
				if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
					t.Fatal("PACKAGE_P2_FIXTURE_DIRECTORY_ERROR")
				}
			}
			control := filepath.Join(t.TempDir(), "validated-assets")
			if err := collectWebAssets(root, control); err != nil {
				t.Fatalf("PACKAGE_P2_VALID_WEB_CONTROL_REJECTED: %v", err)
			}
			before := releaseInputFacts(t, root)
			out := filepath.Join(root, filepath.FromSlash(name))
			err := collectWebAssets(root, out)
			if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
				t.Fatal("PACKAGE_P2_OLD_OUTPUT_CREATED_BEFORE_REJECTION")
			}
			if err == nil {
				t.Fatal("PACKAGE_P2_OVERLAP_ACCEPTED")
			}
			if !reflect.DeepEqual(before, releaseInputFacts(t, root)) {
				t.Fatal("PACKAGE_P2_INPUTS_CHANGED")
			}
		})
	}
}

func TestReleasePrivateSourceSnapshot(t *testing.T) {
	for _, variant := range []string{"checkout_bytes", "original_mutation", "snapshot_tamper", "snapshot_extra", "snapshot_root_extra"} {
		t.Run(variant, func(t *testing.T) {
			root, head := releaseSourceFixture(t)
			if variant == "checkout_bytes" {
				releaseFixtureGit(t, root, "config", "core.autocrlf", "true")
				for _, name := range strings.Split(releaseFixtureGit(t, root, "ls-files"), "\n") {
					if err := os.Remove(filepath.Join(root, filepath.FromSlash(name))); err != nil {
						t.Fatal(err)
					}
				}
				releaseFixtureGit(t, root, "checkout", "--force", "HEAD", "--", ".")
			}
			build, err := newBuildSource(root, head, true, releaseOptions())
			if err != nil {
				t.Fatalf("PACKAGE_PRIVATE_SOURCE_CONTROL_REJECTED: %v", err)
			}
			defer os.RemoveAll(build.dir)
			if pathsOverlap(root, build.dir) {
				t.Fatal("PACKAGE_PRIVATE_SOURCE_OVERLAP")
			}
			if _, err := os.Lstat(filepath.Join(build.dir, ".git")); !os.IsNotExist(err) {
				t.Fatal("PACKAGE_PRIVATE_GIT_PRESENT")
			}
			for _, name := range strings.Split(releaseFixtureGit(t, root, "ls-files"), "\n") {
				if name == ".gitignore" {
					continue
				}
				command := exec.Command("git", "cat-file", "blob", head+":"+name)
				command.Dir = root
				expected, err := command.Output()
				if err != nil {
					t.Fatal("PACKAGE_PRIVATE_NATIVE_BLOB_ERROR")
				}
				actual, err := os.ReadFile(filepath.Join(build.dir, filepath.FromSlash(name)))
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatal("PACKAGE_PRIVATE_BYTES_NOT_RAW_HEAD")
				}
			}
			switch variant {
			case "original_mutation":
				releaseWrite(t, filepath.Join(root, "internal", "fixture", "source.go"), "package fixture\nconst Value = 3\n")
				if build.verify() != nil {
					t.Fatal("PACKAGE_PRIVATE_SOURCE_FOLLOWS_MUTABLE_CHECKOUT")
				}
				if _, err := verifySource(root, head, true); err == nil {
					t.Fatal("PACKAGE_MUTATED_CHECKOUT_ACCEPTED")
				}
			case "snapshot_tamper":
				releaseWrite(t, filepath.Join(build.dir, "cmd", "cpa-cloud", "main.go"), "package main\nfunc main(){}\n")
			case "snapshot_extra":
				releaseWrite(t, filepath.Join(build.dir, "internal", "fixture", "new.bin"), "public extra embed input")
			case "snapshot_root_extra":
				releaseWrite(t, filepath.Join(build.dir, "go.work"), "go 1.26\n")
			}
			if strings.HasPrefix(variant, "snapshot_") {
				output := filepath.Join(t.TempDir(), "must-not-build")
				if buildBinary(build, output, "./cmd/cpa-cloud", releaseOptions()) == nil {
					t.Fatal("PACKAGE_UNPROVEN_SNAPSHOT_COMPILED")
				}
				if _, err := os.Lstat(output); !os.IsNotExist(err) {
					t.Fatal("PACKAGE_SNAPSHOT_FAILURE_CREATED_BINARY")
				}
			}
		})
	}
}

func TestReleaseSourceMoreBoundaries(t *testing.T) {
	for _, variant := range []string{"empty_extra_directory", "hidden_unmodified", "mixed_crlf", "eol_attribute", "head_nonregular"} {
		t.Run(variant, func(t *testing.T) {
			root, head := releaseSourceFixture(t)
			switch variant {
			case "empty_extra_directory":
				if err := os.Mkdir(filepath.Join(root, "internal", "fixture", "owned-empty"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "hidden_unmodified":
				releaseFixtureGit(t, root, "update-index", "--assume-unchanged", "--", "go.mod")
			case "mixed_crlf":
				releaseFixtureGit(t, root, "config", "core.autocrlf", "true")
				releaseWrite(t, filepath.Join(root, "internal", "fixture", "source.go"), "package fixture\r\nconst Value = 1\n")
			case "eol_attribute":
				releaseWrite(t, filepath.Join(root, ".git", "info", "attributes"), "*.go text eol=crlf\n")
			case "head_nonregular":
				oid := releaseFixtureGit(t, root, "rev-parse", "HEAD:internal/fixture/source.go")
				releaseFixtureGit(t, root, "update-index", "--cacheinfo", "120000,"+oid+",internal/fixture/source.go")
				releaseFixtureGit(t, root, "-c", "commit.gpgSign=false", "commit", "-qm", "Synthetic nonregular input")
				head = releaseFixtureGit(t, root, "rev-parse", "HEAD")
			}
			if _, err := verifySource(root, head, true); err == nil {
				t.Fatal("PACKAGE_UNPROVEN_SOURCE_BOUNDARY_ACCEPTED")
			}
		})
	}
}

func TestReleaseModuleSourcePolicy(t *testing.T) {
	for _, variant := range []string{"relative_replace", "absolute_replace", "remote_replace", "unreviewed_dependency"} {
		t.Run(variant, func(t *testing.T) {
			root, _ := releaseSourceFixture(t)
			extra := "\nreplace cpacloud.local/server/unused => ./external\n"
			switch variant {
			case "absolute_replace":
				extra = "\nreplace cpacloud.local/server/unused => " + fmt.Sprintf("%q", filepath.ToSlash(t.TempDir())) + "\n"
			case "remote_replace":
				extra = "\nreplace cpacloud.local/server/unused => example.invalid/unused v0.0.1\n"
			case "unreviewed_dependency":
				extra = "\nrequire example.invalid/unused v0.0.1\n"
			}
			releaseWrite(t, filepath.Join(root, "go.mod"), "module cpacloud.local/server\n\ngo 1.26\n"+extra)
			releaseFixtureGit(t, root, "add", "--", "go.mod")
			releaseFixtureGit(t, root, "-c", "commit.gpgSign=false", "commit", "-qm", "Synthetic module boundary")
			head := releaseFixtureGit(t, root, "rev-parse", "HEAD")
			build, err := newBuildSource(root, head, true, releaseOptions())
			if err == nil || build != nil {
				if build != nil {
					_ = os.RemoveAll(build.dir)
				}
				t.Fatal("PACKAGE_EXTERNAL_MODULE_GRAPH_ACCEPTED")
			}
		})
	}
}

func TestReleaseSourcePathAndEnvironment(t *testing.T) {
	for _, name := range []string{"internal/../outside.go", "cmd\\app\\main.go", "internal/NUL.txt",
		"internal/CON.go", "internal/trailing./file", "internal/a*/file", "internal/a:stream",
		"internal/.git/file", "go.work", "/internal/file"} {
		t.Run(name, func(t *testing.T) {
			if sourcePath(name) {
				t.Fatal("PACKAGE_SOURCE_PATH_ALIAS_ACCEPTED")
			}
		})
	}
	for _, name := range []string{"go.mod", "go.sum", "internal/fixture/public.bin", "cmd/cpa-cloud/main.go"} {
		if !sourcePath(name) {
			t.Fatal("PACKAGE_VALID_SOURCE_PATH_REJECTED")
		}
	}
	t.Setenv("GOFLAGS", "-overlay=untrusted")
	t.Setenv("GOWORK", "untrusted")
	t.Setenv("GO111MODULE", "off")
	t.Setenv("GOAUTH", "untrusted")
	values := map[string]string{}
	for _, item := range buildEnvironment(releaseOptions()) {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	for key, expected := range map[string]string{"GOFLAGS": "", "GOWORK": "off", "GO111MODULE": "on",
		"GOAUTH": "off", "GOPROXY": "off", "GOTOOLCHAIN": "local", "CGO_ENABLED": "0", "GOOS": "linux"} {
		if values[key] != expected {
			t.Fatal("PACKAGE_BUILD_ENVIRONMENT_ESCAPE")
		}
	}
}

func TestReleaseWebDistOutputP2(t *testing.T) {
	for _, name := range []string{"web/dist/zz-collected", "web/dist/assets/zz-collected", "web/dist", "web", "."} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			releaseLockedFrontendFixture(t, root, true)
			control := filepath.Join(t.TempDir(), "valid-assets")
			if err := collectWebAssets(root, control); err != nil {
				t.Fatalf("PACKAGE_DIST_VALID_WEB_REJECTED: %v", err)
			}
			out := filepath.Join(root, filepath.FromSlash(name))
			_, initialErr := os.Lstat(out)
			before := releaseInputFacts(t, root)
			if collectWebAssets(root, out) == nil {
				t.Fatal("PACKAGE_DIST_OUTPUT_OVERLAP_ACCEPTED")
			}
			if os.IsNotExist(initialErr) {
				if _, err := os.Lstat(out); !os.IsNotExist(err) {
					t.Fatal("PACKAGE_DIST_OUTPUT_CREATED")
				}
			}
			if !reflect.DeepEqual(before, releaseInputFacts(t, root)) {
				t.Fatal("PACKAGE_DIST_INPUT_CHANGED")
			}
		})
	}
}

func releaseNoticeFixture(t *testing.T) (*buildSource, []binaryArtifact, string, string) {
	t.Helper()
	root, _ := releaseSourceFixture(t)
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(releaseTestRoot(t), name))
		if err != nil {
			t.Fatal("PACKAGE_NOTICE_FIXTURE_INPUT_ERROR")
		}
		releaseWrite(t, filepath.Join(root, name), string(data))
	}
	releaseWrite(t, filepath.Join(root, "internal", "fixture", "crypto.go"),
		"package fixture\nimport \"golang.org/x/crypto/chacha20poly1305\"\nfunc PublicSize() int { a, err := chacha20poly1305.New(make([]byte, chacha20poly1305.KeySize)); if err != nil { return -1 }; return a.Overhead() }\n")
	for _, name := range []string{"cpa-cloud", "cpa-cloud-backup"} {
		releaseWrite(t, filepath.Join(root, "cmd", name, "main.go"),
			"package main\nimport \"cpacloud.local/server/internal/fixture\"\nvar version = \"fixture\"\nfunc main() { println(fixture.PublicSize()) }\n")
	}
	releaseFixtureGit(t, root, "add", "--all")
	releaseFixtureGit(t, root, "-c", "commit.gpgSign=false", "commit", "-qm", "Synthetic notice fixture")
	head := releaseFixtureGit(t, root, "rev-parse", "HEAD")
	source, err := newBuildSource(root, head, true, releaseOptions())
	if err != nil {
		t.Fatalf("PACKAGE_NOTICE_FIXTURE_SOURCE_ERROR: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(source.dir) })
	options := releaseOptions()
	binaries := []binaryArtifact{}
	for _, name := range []string{"cpa-cloud", "cpa-cloud-backup"} {
		path := filepath.Join(t.TempDir(), name)
		if err := buildBinary(source, path, "./cmd/"+name, options); err != nil {
			t.Fatalf("PACKAGE_NOTICE_FIXTURE_BUILD_ERROR: %v", err)
		}
		binaries = append(binaries, binaryArtifact{Name: name, Path: path})
	}
	command := exec.Command("go", "env", "-json", "GOROOT", "GOMODCACHE", "GOVERSION")
	command.Dir, command.Env = source.dir, buildEnvironment(options)
	data, err := command.Output()
	var actual struct{ GOROOT, GOMODCACHE, GOVERSION string }
	if err != nil || json.Unmarshal(data, &actual) != nil || actual.GOROOT == "" || actual.GOMODCACHE == "" {
		t.Fatal("PACKAGE_NOTICE_FIXTURE_QUERY_ERROR")
	}
	return source, binaries, actual.GOROOT, actual.GOMODCACHE
}

func TestReleaseGoNoticeEnvironmentP2(t *testing.T) {
	source, binaries, actualRoot, actualCache := releaseNoticeFixture(t)
	for _, variant := range []string{"goenv_file", "gomodcache_parent", "goroot_parent", "path_parent"} {
		t.Run(variant, func(t *testing.T) {
			poison := t.TempDir()
			cache, sdk := filepath.Join(poison, "cache"), filepath.Join(poison, "sdk")
			for dep, version := range reviewedGoVersions {
				for _, notice := range goNoticeFiles[dep] {
					releaseWrite(t, filepath.Join(cache, filepath.FromSlash(dep+"@"+version), filepath.FromSlash(notice)), "PUBLIC POISON NOTICE\n")
				}
			}
			for _, name := range []string{"LICENSE", "PATENTS", "VERSION"} {
				body := []byte("PUBLIC POISON RUNTIME NOTICE\n")
				if name == "VERSION" {
					var err error
					body, err = os.ReadFile(filepath.Join(actualRoot, name))
					if err != nil {
						t.Fatal("PACKAGE_NOTICE_FIXTURE_VERSION_ERROR")
					}
				}
				releaseWrite(t, filepath.Join(sdk, name), string(body))
			}
			switch variant {
			case "goenv_file":
				config := filepath.Join(poison, "go-environment")
				releaseWrite(t, config, "GOMODCACHE="+filepath.ToSlash(cache)+"\n")
				t.Setenv("GOENV", config)
			case "gomodcache_parent":
				t.Setenv("GOMODCACHE", cache)
			case "goroot_parent":
				t.Setenv("GOROOT", sdk)
			case "path_parent":
				t.Setenv("PATH", poison)
			}
			command := source.goContext.command("env", "-json", "GOROOT", "GOMODCACHE", "GOVERSION")
			if !filepath.IsAbs(command.Path) || command.Path != source.goContext.executable ||
				command.Dir != source.dir || !reflect.DeepEqual(command.Env, source.goContext.env) {
				t.Fatal("PACKAGE_GO_CONTEXT_NOT_FIXED")
			}
			data, queryErr := command.Output()
			var actual struct{ GOROOT, GOMODCACHE string }
			if queryErr != nil || json.Unmarshal(data, &actual) != nil ||
				!samePath(actual.GOROOT, actualRoot) || !samePath(actual.GOMODCACHE, actualCache) {
				t.Fatal("PACKAGE_BOUND_GO_ENVIRONMENT_CHANGED")
			}
			noticeRoot := filepath.Join(t.TempDir(), "notices")
			_, _, err := collectBinaryGoNotices(source, binaries, releaseOptions(), noticeRoot)
			if err != nil {
				t.Logf("PACKAGE_NOTICE_OLD_OBSERVATION variant=%s collection_error=%v", variant, err)
				t.Fatal("PACKAGE_OLD_LICENSE_ENV_MISMATCH")
			}
			for _, row := range []struct{ copied, expected string }{
				{filepath.Join(noticeRoot, "go-runtime", "LICENSE"), filepath.Join(actualRoot, "LICENSE")},
				{filepath.Join(noticeRoot, "go-modules", safeName("golang.org/x/crypto@"+reviewedGoVersions["golang.org/x/crypto"]), "LICENSE"),
					filepath.Join(actualCache, filepath.FromSlash("golang.org/x/crypto@"+reviewedGoVersions["golang.org/x/crypto"]), "LICENSE")},
			} {
				got, gotErr := os.ReadFile(row.copied)
				expected, expectedErr := os.ReadFile(row.expected)
				if gotErr != nil || expectedErr != nil {
					t.Fatal("PACKAGE_NOTICE_FIXTURE_READ_ERROR")
				}
				if !bytes.Equal(got, expected) {
					t.Logf("PACKAGE_NOTICE_OLD_OBSERVATION variant=%s copied_notice_mismatch=true", variant)
					t.Fatal("PACKAGE_OLD_LICENSE_ENV_MISMATCH")
				}
			}
		})
	}
}

func releaseMutateWeb(t *testing.T, assets string, replaceManifest bool) {
	t.Helper()
	releaseWrite(t, filepath.Join(assets, "web", "index.html"), "<h1>public controlled replacement</h1>")
	if replaceManifest {
		var manifest webAssetManifest
		if err := readJSON(filepath.Join(assets, "WEB-ASSETS.json"), &manifest); err != nil {
			t.Fatal(err)
		}
		files, err := inventoryFiles(assets, map[string]bool{"WEB-ASSETS.json": true})
		if err != nil {
			t.Fatal(err)
		}
		manifest.Files = files
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		releaseWrite(t, filepath.Join(assets, "WEB-ASSETS.json"), string(data)+"\n")
	}
}

func TestReleaseMutableWebP2(t *testing.T) {
	for _, replaceManifest := range []bool{false, true} {
		t.Run(fmt.Sprintf("replace_manifest_%t", replaceManifest), func(t *testing.T) {
			input, assets := t.TempDir(), filepath.Join(t.TempDir(), "assets")
			releaseLockedFrontendFixture(t, input, true)
			if err := collectWebAssets(input, assets); err != nil {
				t.Fatal(err)
			}
			if err := validateWebAssets(assets); err != nil {
				t.Fatal("PACKAGE_WEB_CONTROL_INVALID")
			}
			original, err := os.ReadFile(filepath.Join(assets, "web", "index.html"))
			if err != nil {
				t.Fatal(err)
			}
			fired := false
			source, err := newWebBuildSourceWithCopy(assets, func(from, to string, mode fs.FileMode) error {
				if !fired {
					fired = true
					releaseMutateWeb(t, assets, replaceManifest)
				}
				return copyFile(from, to, mode)
			})
			if !fired {
				t.Fatal("PACKAGE_WEB_COPY_BOUNDARY_NOT_REACHED")
			}
			if err == nil || source != nil {
				if source != nil {
					_ = os.RemoveAll(source.dir)
				}
				t.Fatal("PACKAGE_MUTABLE_WEB_SNAPSHOT_ACCEPTED")
			}
			changed, err := os.ReadFile(filepath.Join(assets, "web", "index.html"))
			if err != nil || bytes.Equal(changed, original) {
				t.Fatal("PACKAGE_WEB_MUTATION_FIXTURE_FAILED")
			}
		})
	}
}

func TestReleaseWebSnapshotBoundaries(t *testing.T) {
	for _, variant := range []string{"origin_after_freeze", "private_file", "private_manifest_and_file", "mid_stage_file", "stage_manifest_and_file", "stage_extra"} {
		t.Run(variant, func(t *testing.T) {
			input, assets := t.TempDir(), filepath.Join(t.TempDir(), "assets")
			releaseLockedFrontendFixture(t, input, true)
			if err := collectWebAssets(input, assets); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(filepath.Join(assets, "web", "index.html"))
			if err != nil {
				t.Fatal(err)
			}
			initialManifest, err := os.ReadFile(filepath.Join(assets, "WEB-ASSETS.json"))
			if err != nil {
				t.Fatal(err)
			}
			source, err := newWebBuildSource(assets)
			if err != nil {
				t.Fatalf("PACKAGE_FROZEN_WEB_CONTROL_REJECTED: %v", err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(source.dir) })
			stage := t.TempDir()
			switch variant {
			case "private_file", "private_manifest_and_file":
				releaseMutateWeb(t, source.dir, variant == "private_manifest_and_file")
				if variant == "private_manifest_and_file" && validateWebAssets(source.dir) != nil {
					t.Fatal("PACKAGE_SELF_CONSISTENT_PRIVATE_WEB_FIXTURE_FAILED")
				}
				if source.copyToStage(stage) == nil {
					t.Fatal("PACKAGE_PRIVATE_WEB_TAMPER_ACCEPTED")
				}
				return
			case "mid_stage_file":
				fired := false
				err := source.copyToStageWith(stage, func(from, to string, mode fs.FileMode) error {
					if err := copyFile(from, to, mode); err != nil {
						return err
					}
					if filepath.Base(to) == "index.html" {
						fired = true
						releaseWrite(t, to, "<h1>public changed stage file</h1>")
					}
					return nil
				})
				if !fired || err == nil {
					t.Fatal("PACKAGE_MID_STAGE_WEB_TAMPER_ACCEPTED")
				}
				return
			case "origin_after_freeze":
				releaseMutateWeb(t, assets, true)
			}
			if err := source.copyToStage(stage); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(stage, "web", "index.html"))
			manifest, manifestErr := os.ReadFile(filepath.Join(stage, "WEB-ASSETS.json"))
			if err != nil || manifestErr != nil || !bytes.Equal(got, original) || !bytes.Equal(manifest, initialManifest) {
				t.Fatal("PACKAGE_STAGE_NOT_BOUND_TO_INITIAL_WEB")
			}
			if variant == "stage_manifest_and_file" {
				releaseMutateWeb(t, stage, true)
				if validateWebAssets(stage) != nil {
					t.Fatal("PACKAGE_SELF_CONSISTENT_STAGED_WEB_FIXTURE_FAILED")
				}
			} else if variant == "stage_extra" {
				releaseWrite(t, filepath.Join(stage, "web", "extra.js"), "public extra staged input")
			}
			if variant != "origin_after_freeze" && source.verifyStage(stage) == nil {
				t.Fatal("PACKAGE_STAGED_WEB_CLOSURE_TAMPER_ACCEPTED")
			}
		})
	}
}

func TestReleaseGoContextParentPoison(t *testing.T) {
	root, head := releaseSourceFixture(t)
	baseline, err := newBuildSource(root, head, true, releaseOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(baseline.dir) })
	poison := t.TempDir()
	config := filepath.Join(poison, "go-environment")
	releaseWrite(t, config, "GOROOT="+filepath.ToSlash(poison)+"\nGOMODCACHE="+filepath.ToSlash(poison)+"\n")
	t.Setenv("GOENV", config)
	t.Setenv("GOROOT", poison)
	t.Setenv("GOMODCACHE", poison)
	t.Setenv("GOPATH", poison)
	source, err := newBuildSource(root, head, true, releaseOptions())
	if err != nil {
		t.Fatalf("PACKAGE_POISONED_PARENT_CONTEXT_REJECTED: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(source.dir) })
	if !samePath(source.goContext.goRoot, baseline.goContext.goRoot) ||
		!samePath(source.goContext.moduleCache, baseline.goContext.moduleCache) ||
		source.goContext.executable != baseline.goContext.executable {
		t.Fatal("PACKAGE_GO_CONTEXT_INHERITED_PARENT_POISON")
	}
	options := releaseOptions()
	options.targetOS = "darwin"
	output := filepath.Join(t.TempDir(), "must-not-build")
	if buildBinary(source, output, "./cmd/cpa-cloud", options) == nil {
		t.Fatal("PACKAGE_DIFFERENT_GO_CONTEXT_TARGET_ACCEPTED")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatal("PACKAGE_CONTEXT_FAILURE_CREATED_BINARY")
	}
}

func TestReleaseSourceIdentity(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(root) == "scripts" {
		root = filepath.Dir(root)
	}
	head := gitCommit(root)
	if len(head) != 40 {
		t.Fatal("PACKAGE_TEST_CHECKOUT_ID")
	}
	t.Setenv("GITHUB_SHA", strings.Repeat("f", 40))
	actual, err := verifySource(root, head, true)
	if err != nil || actual != head {
		t.Fatalf("PACKAGE_REAL_SOURCE_REJECTED: %v", err)
	}
	if _, err := verifySource(root, strings.Repeat("f", 40), true); err == nil {
		t.Fatal("PACKAGE_FORGED_SOURCE_ACCEPTED")
	}
	if _, err := verifySource(t.TempDir(), head, true); err == nil {
		t.Fatal("PACKAGE_UNKNOWN_SOURCE_ACCEPTED")
	}
	if _, err := verifySource(root, "", true); err == nil {
		t.Fatal("PACKAGE_SOURCE_REQUIRED")
	}
}
