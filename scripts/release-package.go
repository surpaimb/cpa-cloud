// Command release-package builds the bounded CPA Cloud preview release payload.
// It intentionally packages only the server binary, static web output, documentation,
// and reviewed third-party license texts. It never reads a data directory.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	rdebug "runtime/debug"
	"sort"
	"strings"
	"time"
)

var previewTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-preview\.[0-9]+$`)

var goNoticeFiles = map[string][]string{
	"github.com/dustin/go-humanize":    {"LICENSE"},
	"github.com/google/uuid":           {"LICENSE"},
	"github.com/mattn/go-isatty":       {"LICENSE"},
	"github.com/ncruces/go-strftime":   {"LICENSE"},
	"github.com/remyoudompheng/bigfft": {"LICENSE"},
	"golang.org/x/crypto":              {"LICENSE", "PATENTS"},
	"golang.org/x/exp":                 {"LICENSE", "PATENTS"},
	"golang.org/x/sys":                 {"LICENSE", "PATENTS"},
	"modernc.org/libc":                 {"LICENSE", "LICENSE-GO", "honnef.co/go/netdb/LICENSE"},
	"modernc.org/mathutil":             {"LICENSE", "mersenne/LICENSE"},
	"modernc.org/memory":               {"LICENSE", "LICENSE-GO", "LICENSE-MMAP-GO"},
	"modernc.org/sqlite":               {"LICENSE", "SQLITE-LICENSE"},
}

type frontendDependency struct {
	Name       string
	Version    string
	License    string
	NoticeFile string
	Role       string
}

var frontendRuntimeDependencies = []frontendDependency{
	{Name: "react", Version: "19.3.0", License: "MIT", NoticeFile: "LICENSE", Role: "bundled browser runtime"},
	{Name: "react-dom", Version: "19.3.0", License: "MIT", NoticeFile: "LICENSE", Role: "bundled browser runtime"},
	{Name: "scheduler", Version: "0.28.0", License: "MIT", NoticeFile: "LICENSE", Role: "bundled browser runtime"},
	{Name: "vite", Version: "8.3.0", License: "MIT", NoticeFile: "LICENSE.md", Role: "generated modulepreload/runtime helper"},
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release-package:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("expected validate-tag, web-assets, or package command")
	}
	switch args[0] {
	case "validate-tag":
		set := flag.NewFlagSet("validate-tag", flag.ContinueOnError)
		tag := set.String("tag", os.Getenv("RELEASE_TAG"), "preview release tag (defaults to RELEASE_TAG)")
		if err := set.Parse(args[1:]); err != nil {
			return err
		}
		return validatePreviewTag(*tag)
	case "web-assets":
		set := flag.NewFlagSet("web-assets", flag.ContinueOnError)
		source := set.String("source", ".", "repository root")
		output := set.String("output", "", "new output directory")
		if err := set.Parse(args[1:]); err != nil {
			return err
		}
		return collectWebAssets(*source, *output)
	case "package":
		set := flag.NewFlagSet("package", flag.ContinueOnError)
		source := set.String("source", ".", "repository root")
		webAssets := set.String("web-assets", "", "validated web-assets directory")
		output := set.String("output", "", "output directory")
		version := set.String("version", os.Getenv("RELEASE_TAG"), "preview release tag (defaults to RELEASE_TAG)")
		targetOS := set.String("target-os", "", "Go target OS")
		targetArch := set.String("target-arch", "", "Go target architecture")
		assetOS := set.String("asset-os", "", "user-facing OS name")
		if err := set.Parse(args[1:]); err != nil {
			return err
		}
		return buildPackage(packageOptions{
			source: *source, webAssets: *webAssets, output: *output, version: *version,
			targetOS: *targetOS, targetArch: *targetArch, assetOS: *assetOS,
		})
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func validatePreviewTag(tag string) error {
	if !previewTagPattern.MatchString(tag) {
		return fmt.Errorf("tag %q must match vMAJOR.MINOR.PATCH-preview.N", tag)
	}
	return nil
}

func collectWebAssets(source, output string) error {
	if strings.TrimSpace(output) == "" {
		return errors.New("--output is required")
	}
	root, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	out, err := prepareEmptyDirectory(output)
	if err != nil {
		return err
	}
	webDist := filepath.Join(root, "web", "dist")
	if _, err := os.Stat(filepath.Join(webDist, "index.html")); err != nil {
		return fmt.Errorf("web build is missing index.html: %w", err)
	}
	if err := copyTree(webDist, filepath.Join(out, "web")); err != nil {
		return fmt.Errorf("copy web build: %w", err)
	}

	licenseRoot := filepath.Join(out, "THIRD-PARTY-LICENSES", "npm")
	manifest := []string{"Browser runtime dependencies and generated runtime helpers included in web/:"}
	for _, expected := range frontendRuntimeDependencies {
		packageDir := filepath.Join(root, "web", "node_modules", filepath.FromSlash(expected.Name))
		metadata, err := readNPMMetadata(filepath.Join(packageDir, "package.json"))
		if err != nil {
			return err
		}
		if metadata.Name != expected.Name || metadata.Version != expected.Version || metadata.License != expected.License {
			return fmt.Errorf("frontend dependency review is stale for %s: found %s %s (%s)", metadata.Name, metadata.Version, metadata.License, packageDir)
		}
		destination := filepath.Join(licenseRoot, safeName(expected.Name+"@"+expected.Version))
		if err := copyFile(filepath.Join(packageDir, expected.NoticeFile), filepath.Join(destination, expected.NoticeFile), 0o644); err != nil {
			return fmt.Errorf("copy %s license: %w", expected.Name, err)
		}
		if err := copyFile(filepath.Join(packageDir, "package.json"), filepath.Join(destination, "package.json"), 0o644); err != nil {
			return fmt.Errorf("copy %s package metadata: %w", expected.Name, err)
		}
		manifest = append(manifest, fmt.Sprintf("- %s@%s | %s | declared license: %s | notice: THIRD-PARTY-LICENSES/npm/%s/%s", expected.Name, expected.Version, expected.Role, expected.License, safeName(expected.Name+"@"+expected.Version), expected.NoticeFile))
	}
	manifest = append(manifest, "", "Build-only packages and node_modules are not included in the release archive.")
	return os.WriteFile(filepath.Join(out, "FRONTEND-DEPENDENCIES.txt"), []byte(strings.Join(manifest, "\n")+"\n"), 0o644)
}

type npmMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	License string `json:"license"`
}

func readNPMMetadata(path string) (npmMetadata, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return npmMetadata{}, fmt.Errorf("read npm package metadata %s: %w", path, err)
	}
	var metadata npmMetadata
	if err := json.Unmarshal(b, &metadata); err != nil {
		return npmMetadata{}, fmt.Errorf("parse npm package metadata %s: %w", path, err)
	}
	return metadata, nil
}

type packageOptions struct {
	source, webAssets, output, version, targetOS, targetArch, assetOS string
}

func buildPackage(options packageOptions) error {
	if err := validatePreviewTag(options.version); err != nil {
		return err
	}
	validTargets := map[string]string{"windows": "windows", "linux": "linux", "darwin": "macos"}
	expectedAssetOS, ok := validTargets[options.targetOS]
	if !ok || expectedAssetOS != options.assetOS {
		return fmt.Errorf("unsupported target/asset OS pair %q/%q", options.targetOS, options.assetOS)
	}
	if options.targetArch != "amd64" && options.targetArch != "arm64" {
		return fmt.Errorf("unsupported architecture %q", options.targetArch)
	}
	if runtime.GOOS != options.targetOS || runtime.GOARCH != options.targetArch {
		return fmt.Errorf("native package required: runner is %s/%s, target is %s/%s", runtime.GOOS, runtime.GOARCH, options.targetOS, options.targetArch)
	}
	if strings.TrimSpace(options.webAssets) == "" || strings.TrimSpace(options.output) == "" {
		return errors.New("--web-assets and --output are required")
	}
	root, err := filepath.Abs(options.source)
	if err != nil {
		return err
	}
	webRoot, err := filepath.Abs(options.webAssets)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(webRoot, "web", "index.html")); err != nil {
		return fmt.Errorf("validated web assets are incomplete: %w", err)
	}
	out, err := prepareOutputDirectory(options.output)
	if err != nil {
		return err
	}
	stageParent, err := os.MkdirTemp("", "cpa-cloud-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageParent)

	baseName := fmt.Sprintf("cpa-cloud_%s_%s_%s", options.version, options.assetOS, options.targetArch)
	stage := filepath.Join(stageParent, baseName)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	binaryName := "cpa-cloud"
	if options.targetOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(stage, binaryName)
	if err := buildServer(root, binaryPath, options); err != nil {
		return err
	}
	if err := executeHelp(binaryPath); err != nil {
		return fmt.Errorf("native binary execution check failed: %w", err)
	}
	if err := copyTree(filepath.Join(webRoot, "web"), filepath.Join(stage, "web")); err != nil {
		return fmt.Errorf("copy validated web assets: %w", err)
	}
	if err := copyTree(filepath.Join(webRoot, "THIRD-PARTY-LICENSES", "npm"), filepath.Join(stage, "THIRD-PARTY-LICENSES", "npm")); err != nil {
		return fmt.Errorf("copy frontend notices: %w", err)
	}
	if err := copyFile(filepath.Join(webRoot, "FRONTEND-DEPENDENCIES.txt"), filepath.Join(stage, "FRONTEND-DEPENDENCIES.txt"), 0o644); err != nil {
		return err
	}
	for _, name := range []string{"README.md", "README.en.md", "CONTRIBUTING.md", "AGENTS.md", "THIRD_PARTY_NOTICES.md"} {
		sourcePath := filepath.Join(root, name)
		if err := copyFile(sourcePath, filepath.Join(stage, name), 0o644); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}
	if err := copyTrackedDirectory(root, "docs", filepath.Join(stage, "docs")); err != nil {
		return fmt.Errorf("copy tracked documentation: %w", err)
	}
	goManifest, err := collectGoNotices(binaryPath, filepath.Join(stage, "THIRD-PARTY-LICENSES"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "GO-DEPENDENCIES.txt"), []byte(goManifest), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "VERSION"), []byte(options.version+"\n"), 0o644); err != nil {
		return err
	}
	commit := strings.TrimSpace(os.Getenv("GITHUB_SHA"))
	if commit == "" {
		commit = gitCommit(root)
	}
	buildInfo := fmt.Sprintf("Version: %s\nCommit: %s\nTarget: %s/%s\nNative package host: %s/%s\nGo toolchain: %s\nNative --help execution check: passed\nScope: build/package verification only; no target deployment or end-to-end target-host claim.\n", options.version, commit, options.assetOS, options.targetArch, runtime.GOOS, runtime.GOARCH, runtime.Version())
	if err := os.WriteFile(filepath.Join(stage, "BUILD-INFO.txt"), []byte(buildInfo), 0o644); err != nil {
		return err
	}
	if err := writeTreeChecksums(stage); err != nil {
		return err
	}

	extension := ".tar.gz"
	if options.targetOS == "windows" {
		extension = ".zip"
	}
	archivePath := filepath.Join(out, baseName+extension)
	if _, err := os.Stat(archivePath); err == nil {
		return fmt.Errorf("refusing to overwrite existing archive %s", archivePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if options.targetOS == "windows" {
		err = writeZip(archivePath, stageParent, baseName)
	} else {
		err = writeTarGz(archivePath, stageParent, baseName)
	}
	if err != nil {
		return err
	}
	digest, err := hashFile(archivePath)
	if err != nil {
		return err
	}
	sidecar := archivePath + ".sha256"
	if err := os.WriteFile(sidecar, []byte(fmt.Sprintf("%s  %s\n", digest, filepath.Base(archivePath))), 0o644); err != nil {
		return err
	}
	if err := removeTemporaryTree(stageParent); err != nil {
		return err
	}
	return nil
}

func removeTemporaryTree(path string) error {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if err := os.RemoveAll(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	return fmt.Errorf("remove temporary release tree %s: %w", path, lastErr)
}

func buildServer(root, destination string, options packageOptions) error {
	command := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.version="+options.version, "-o", destination, "./cmd/cpa-cloud")
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+options.targetOS, "GOARCH="+options.targetArch)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("build server: %w", err)
	}
	if options.targetOS != "windows" {
		if err := os.Chmod(destination, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func executeHelp(binary string) error {
	command := exec.Command(binary, "--help")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func collectGoNotices(binary, noticeRoot string) (string, error) {
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return "", fmt.Errorf("read Go build information: %w", err)
	}
	goRoot := strings.TrimSpace(runOutput("go", "env", "GOROOT"))
	if goRoot == "" {
		return "", errors.New("go env GOROOT returned an empty path")
	}
	for _, name := range []string{"LICENSE", "PATENTS", "VERSION"} {
		if err := copyFile(filepath.Join(goRoot, name), filepath.Join(noticeRoot, "go-runtime", name), 0o644); err != nil {
			return "", fmt.Errorf("copy Go runtime %s: %w", name, err)
		}
	}
	lines := []string{fmt.Sprintf("Go runtime: %s", info.GoVersion), "Go modules linked into the server binary:"}
	modules := append([]*rdebug.Module(nil), info.Deps...)
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	for _, module := range modules {
		if module.Replace != nil {
			return "", fmt.Errorf("linked Go module %s@%s uses an unreviewed replacement", module.Path, module.Version)
		}
		files, reviewed := goNoticeFiles[module.Path]
		if !reviewed {
			return "", fmt.Errorf("linked Go module %s@%s has no reviewed release notice mapping", module.Path, module.Version)
		}
		dir, err := moduleDirectory(module.Path, module.Version)
		if err != nil {
			return "", err
		}
		destination := filepath.Join(noticeRoot, "go-modules", safeName(module.Path+"@"+module.Version))
		for _, relative := range files {
			if err := copyFile(filepath.Join(dir, filepath.FromSlash(relative)), filepath.Join(destination, filepath.FromSlash(relative)), 0o644); err != nil {
				return "", fmt.Errorf("copy notice %s for %s@%s: %w", relative, module.Path, module.Version, err)
			}
		}
		lines = append(lines, fmt.Sprintf("- %s@%s | notices: THIRD-PARTY-LICENSES/go-modules/%s", module.Path, module.Version, safeName(module.Path+"@"+module.Version)))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func moduleDirectory(path, version string) (string, error) {
	if path == "" || version == "" || version == "(devel)" {
		return "", fmt.Errorf("module %q has no immutable version", path)
	}
	moduleCache := strings.TrimSpace(runOutput("go", "env", "GOMODCACHE"))
	if moduleCache == "" {
		return "", errors.New("go env GOMODCACHE returned an empty path")
	}
	// Every reviewed release dependency currently has a lower-case module path and
	// version, so its extracted cache directory needs no Go upper-case escaping.
	if path != strings.ToLower(path) || version != strings.ToLower(version) {
		return "", fmt.Errorf("module cache escaping for %s@%s has not been reviewed", path, version)
	}
	candidate := filepath.Join(moduleCache, filepath.FromSlash(path+"@"+version))
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate, nil
	}
	command := exec.Command("go", "mod", "download", "-json", path+"@"+version)
	output, commandErr := command.Output()
	if commandErr != nil {
		return "", fmt.Errorf("module %s@%s is absent from the cache and download failed: %w", path, version, commandErr)
	}
	var metadata struct {
		Dir   string
		Error *struct{ Err string }
	}
	if err := json.Unmarshal(output, &metadata); err != nil {
		return "", fmt.Errorf("parse module metadata for %s@%s: %w", path, version, err)
	}
	if metadata.Error != nil || metadata.Dir == "" {
		message := "module directory is unavailable"
		if metadata.Error != nil && metadata.Error.Err != "" {
			message = metadata.Error.Err
		}
		return "", fmt.Errorf("resolve module %s@%s: %s", path, version, message)
	}
	return metadata.Dir, nil
}

func prepareEmptyDirectory(path string) (string, error) {
	out, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(out, 0o755); err != nil {
		return "", fmt.Errorf("create new output directory %s: %w", out, err)
	}
	return out, nil
}

func prepareOutputDirectory(path string) (string, error) {
	out, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	return out, nil
}

func copyTree(source, destination string) error {
	root, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("release input must not contain symlinks: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyTrackedDirectory(root, repositoryPath, destination string) error {
	command := exec.Command("git", "ls-files", "-z", "--", repositoryPath)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return err
	}
	files := strings.Split(string(output), "\x00")
	copied := 0
	for _, relative := range files {
		if relative == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(relative))
		prefix := filepath.Clean(repositoryPath) + string(os.PathSeparator)
		if clean == filepath.Clean(repositoryPath) || !strings.HasPrefix(clean, prefix) {
			return fmt.Errorf("tracked path escaped %s: %s", repositoryPath, relative)
		}
		source := filepath.Join(root, clean)
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tracked documentation must be a regular file: %s", source)
		}
		relativeWithin, err := filepath.Rel(repositoryPath, clean)
		if err != nil {
			return err
		}
		if err := copyFile(source, filepath.Join(destination, relativeWithin), info.Mode().Perm()); err != nil {
			return err
		}
		copied++
	}
	if copied == 0 {
		return errors.New("no tracked documentation files found")
	}
	return nil
}

func writeTreeChecksums(root string) error {
	type item struct{ path, relative string }
	items := make([]item, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("release tree must not contain symlinks: %s", path)
		}
		if entry.IsDir() || entry.Name() == "SHA256SUMS.txt" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		items = append(items, item{path: path, relative: filepath.ToSlash(relative)})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].relative < items[j].relative })
	var output strings.Builder
	for _, item := range items {
		digest, err := hashFile(item.path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&output, "%s  %s\n", digest, item.relative)
	}
	return os.WriteFile(filepath.Join(root, "SHA256SUMS.txt"), []byte(output.String()), 0o644)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeZip(destination, parent, rootName string) error {
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(f)
	err = walkArchiveTree(filepath.Join(parent, rootName), func(path, relative string, info fs.FileInfo) error {
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join(rootName, relative))
		if info.IsDir() {
			header.Name += "/"
		} else {
			header.Method = zip.Deflate
		}
		header.Modified = time.Unix(0, 0).UTC()
		entry, err := writer.CreateHeader(header)
		if err != nil || info.IsDir() {
			return err
		}
		return copyIntoArchive(path, entry)
	})
	closeArchiveErr := writer.Close()
	closeFileErr := f.Close()
	if err != nil {
		return err
	}
	if closeArchiveErr != nil {
		return closeArchiveErr
	}
	return closeFileErr
}

func writeTarGz(destination, parent, rootName string) error {
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	gzipWriter := gzip.NewWriter(f)
	gzipWriter.Header.ModTime = time.Unix(0, 0).UTC()
	tarWriter := tar.NewWriter(gzipWriter)
	err = walkArchiveTree(filepath.Join(parent, rootName), func(path, relative string, info fs.FileInfo) error {
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join(rootName, relative))
		header.ModTime = time.Unix(0, 0).UTC()
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		if err := tarWriter.WriteHeader(header); err != nil || info.IsDir() {
			return err
		}
		return copyIntoArchive(path, tarWriter)
	})
	closeTarErr := tarWriter.Close()
	closeGzipErr := gzipWriter.Close()
	closeFileErr := f.Close()
	if err != nil {
		return err
	}
	if closeTarErr != nil {
		return closeTarErr
	}
	if closeGzipErr != nil {
		return closeGzipErr
	}
	return closeFileErr
}

func walkArchiveTree(root string, visit func(path, relative string, info fs.FileInfo) error) error {
	return filepath.Walk(root, func(path string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("release tree must not contain symlinks: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			relative = ""
		}
		return visit(path, relative, info)
	})
}

func copyIntoArchive(path string, destination io.Writer) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	_, err = io.Copy(destination, input)
	return err
}

func runOutput(name string, args ...string) string {
	output, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return string(output)
}

func gitCommit(root string) string {
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func safeName(value string) string {
	var output strings.Builder
	for _, character := range value {
		if character == '/' || character == '\\' || character == ':' || character == '@' {
			output.WriteByte('_')
		} else {
			output.WriteRune(character)
		}
	}
	return output.String()
}
