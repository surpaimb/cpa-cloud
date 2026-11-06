// Command release-package builds bounded preview payloads and the v0.1.0
// Linux amd64 server/Web/backup-CLI payload. Independently extended from the
// authorized CPA Cloud release contract and public Go standard-library APIs.
// No reference-product implementation is used. No data directory is read.
package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var previewTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-preview\.[0-9]+$`)
var sourceSHAPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

var goNoticeFiles = map[string][]string{
	"github.com/dustin/go-humanize":    {"LICENSE"},
	"github.com/google/uuid":           {"LICENSE"},
	"github.com/mattn/go-isatty":       {"LICENSE"},
	"github.com/ncruces/go-strftime":   {"LICENSE"},
	"github.com/remyoudompheng/bigfft": {"LICENSE"},
	"golang.org/x/crypto":              {"LICENSE", "PATENTS"},
	"golang.org/x/exp":                 {"LICENSE", "PATENTS"},
	"golang.org/x/sys":                 {"LICENSE", "PATENTS"},
	"modernc.org/libc":                 {"LICENSE", "LICENSE-3RD-PARTY.md"},
	"modernc.org/mathutil":             {"LICENSE", "mersenne/LICENSE"},
	"modernc.org/memory":               {"LICENSE", "LICENSE-GO", "LICENSE-MMAP-GO"},
	"modernc.org/sqlite":               {"LICENSE", "SQLITE-LICENSE"},
}
var reviewedGoVersions = map[string]string{
	"github.com/dustin/go-humanize":    "v1.0.1",
	"github.com/google/uuid":           "v1.6.0",
	"github.com/mattn/go-isatty":       "v0.0.20",
	"github.com/ncruces/go-strftime":   "v1.0.0",
	"github.com/remyoudompheng/bigfft": "v0.0.0-20230129092748-24d4a6f8daec",
	"golang.org/x/crypto":              "v0.42.0",
	"golang.org/x/exp":                 "v0.0.0-20251023183803-a4bb9ffd2546",
	"golang.org/x/sys":                 "v0.42.0",
	"modernc.org/libc":                 "v1.70.0",
	"modernc.org/mathutil":             "v1.7.1",
	"modernc.org/memory":               "v1.11.0",
	"modernc.org/sqlite":               "v1.46.2",
}

type frontendDependency struct {
	Name, Version, License, NoticeFile, Role string
	AdditionalNoticeFiles                    []string
	NoticeSHA256                             map[string]string
}

var frontendRuntimeDependencies = []frontendDependency{
	{Name: "react", Version: "19.3.0", License: "MIT", NoticeFile: "LICENSE", Role: "bundled browser runtime"},
	{Name: "react-dom", Version: "19.3.0", License: "MIT", NoticeFile: "LICENSE", Role: "bundled browser runtime"},
	{Name: "scheduler", Version: "0.28.0", License: "MIT", NoticeFile: "LICENSE", Role: "bundled browser runtime"},
	{Name: "vite", Version: "8.3.0", License: "MIT", NoticeFile: "LICENSE.md", Role: "generated modulepreload/runtime helper"},
	{Name: "rolldown", Version: "1.2.9", License: "MIT", NoticeFile: "LICENSE", Role: "modulepreload helper via Vite's rolldown/experimental delegation",
		AdditionalNoticeFiles: []string{"THIRD-PARTY-LICENSE"},
		NoticeSHA256: map[string]string{
			"LICENSE":             "23ecfff35a5a2e80d92142f75228912c3b1abc4b5a8337a821ff4397e2f9f734",
			"THIRD-PARTY-LICENSE": "a877291d800ed43692f3f9ae09d8e01cc6f7293ad39d43896059c188ffbb8b7c",
		}},
}

// SHA256 of the reviewed committed web/bun.lock, normalizing CRLF to LF only.
// Its exact graph pins Vite 8.3.0, rolldown 1.2.9 and the browser runtimes.
const reviewedFrontendLockSHA256 = "a4eb191ae92b5545e6f8deb8ad504ebecb44c929a5d4146acf225988a27c2ade"

func frontendNoticeFiles(d frontendDependency) []string {
	return append([]string{d.NoticeFile}, d.AdditionalNoticeFiles...)
}

func verifyFrontendNotice(d frontendDependency, name, path string) error {
	if len(d.NoticeSHA256) == 0 {
		return nil
	}
	expected, ok := d.NoticeSHA256[name]
	if !ok {
		return errors.New("frontend notice has no reviewed content digest")
	}
	digest, err := hashFile(path)
	if err != nil || digest != expected {
		return errors.New("frontend notice differs from reviewed full text")
	}
	return nil
}

func validateFrontendLock(root string) error {
	path := filepath.Join(root, "web", "bun.lock")
	if err := noSymlinkComponents(path, false); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("frontend source lock is missing or nonregular")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(strings.ReplaceAll(string(data), "\r\n", "\n")))
	if hex.EncodeToString(digest[:]) != reviewedFrontendLockSHA256 {
		return errors.New("frontend source lock differs from reviewed graph")
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release-package:", err)
		os.Exit(1)
	}
}
func parseFlags(set *flag.FlagSet, args []string) error {
	seen := map[string]bool{}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			name := strings.SplitN(strings.TrimLeft(arg, "-"), "=", 2)[0]
			if seen[name] {
				return fmt.Errorf("duplicate flag %q", name)
			}
			seen[name] = true
		}
	}
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	return nil
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("expected validate-tag, web-assets, or package command")
	}
	switch args[0] {
	case "validate-tag":
		set := flag.NewFlagSet("validate-tag", flag.ContinueOnError)
		tag := set.String("tag", os.Getenv("RELEASE_TAG"), "preview tag or exact v0.1.0")
		if err := parseFlags(set, args[1:]); err != nil {
			return err
		}
		return validateReleaseTag(*tag)
	case "web-assets":
		set := flag.NewFlagSet("web-assets", flag.ContinueOnError)
		source := set.String("source", ".", "repository root")
		output := set.String("output", "", "new output directory")
		if err := parseFlags(set, args[1:]); err != nil {
			return err
		}
		return collectWebAssets(*source, *output)
	case "package":
		set := flag.NewFlagSet("package", flag.ContinueOnError)
		source := set.String("source", ".", "repository root")
		web := set.String("web-assets", "", "validated web-assets directory")
		output := set.String("output", "", "output directory")
		version := set.String("version", os.Getenv("RELEASE_TAG"), "preview tag or exact v0.1.0")
		targetOS := set.String("target-os", "", "Go target OS")
		arch := set.String("target-arch", "", "Go target architecture")
		assetOS := set.String("asset-os", "", "user-facing OS name")
		sha := set.String("source-sha", "", "40-hex actual checkout HEAD (required for v0.1.0)")
		backup := set.Bool("include-backup-cli", false, "include cpa-cloud-backup (required for v0.1.0)")
		if err := parseFlags(set, args[1:]); err != nil {
			return err
		}
		return buildPackage(packageOptions{source: *source, webAssets: *web, output: *output, version: *version,
			targetOS: *targetOS, targetArch: *arch, assetOS: *assetOS, sourceSHA: *sha, includeBackupCLI: *backup})
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
func validateReleaseTag(tag string) error {
	if tag == "v0.1.0" {
		return nil
	}
	return validatePreviewTag(tag)
}

type packageOptions struct {
	source, webAssets, output, version, targetOS, targetArch, assetOS, sourceSHA string
	includeBackupCLI                                                             bool
}

func validatePackageOptions(o packageOptions) error {
	if err := validateReleaseTag(o.version); err != nil {
		return err
	}
	targets := map[string]string{"windows": "windows", "linux": "linux", "darwin": "macos"}
	if targets[o.targetOS] == "" || targets[o.targetOS] != o.assetOS {
		return errors.New("unsupported target/asset OS pair")
	}
	if o.targetArch != "amd64" && o.targetArch != "arm64" {
		return errors.New("unsupported target architecture")
	}
	if o.sourceSHA != "" && !sourceSHAPattern.MatchString(o.sourceSHA) {
		return errors.New("--source-sha must be exactly 40 hex characters")
	}
	if o.version == "v0.1.0" {
		if o.targetOS != "linux" || o.targetArch != "amd64" || !o.includeBackupCLI || o.sourceSHA == "" {
			return errors.New("v0.1.0 requires linux/amd64, --include-backup-cli and --source-sha")
		}
		if runtime.Version() != "go1.26.9" {
			return errors.New("v0.1.0 requires Go 1.26.9")
		}
	} else if runtime.GOOS != o.targetOS || runtime.GOARCH != o.targetArch {
		return fmt.Errorf("native preview package required: host %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

// Independently authored source admission: no status/clean-filter proof. All
// project Go and embed inputs are bound to unfiltered fixed HEAD blobs.
func verifySource(root, requested string, required bool) (string, error) {
	source, err := inspectSource(root, requested, required)
	if err != nil {
		return "", err
	}
	return source.head, nil
}

type boundFile struct {
	mode, oid string
	data      []byte
}
type boundSource struct {
	head  string
	files map[string]boundFile
	order []string
	crlf  bool
}

var binarySourceRoots = []string{"go.mod", "go.sum", "cmd", "internal"}

func sourceGit(root string, args ...string) *exec.Cmd {
	command := exec.Command("git", append([]string{"--no-replace-objects", "--no-optional-locks", "-c", "core.fsmonitor=false"}, args...)...)
	command.Dir = root
	for _, value := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(value, "=", 2)[0])
		if strings.HasPrefix(key, "GIT_") && key != "GIT_CONFIG_NOSYSTEM" &&
			key != "GIT_CONFIG_GLOBAL" && key != "GIT_CONFIG_SYSTEM" {
			continue
		}
		command.Env = append(command.Env, value)
	}
	command.Env = append(command.Env, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	return command
}

func sourceRecords(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != 0 || len(data) > 8<<20 {
		return nil, errors.New("invalid source index/tree framing")
	}
	records := strings.Split(string(data[:len(data)-1]), "\x00")
	if len(records) > 10000 {
		return nil, errors.New("source file count exceeds bound")
	}
	return records, nil
}

func sourcePath(name string) bool {
	if name != "go.mod" && name != "go.sum" &&
		!strings.HasPrefix(name, "cmd/") && !strings.HasPrefix(name, "internal/") {
		return false
	}
	if filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) != name ||
		strings.ContainsAny(name, "\\:\x00\r\n\t<>\"|?*") || !utf8.ValidString(name) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") ||
			strings.TrimRight(part, ". ") != part {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			(len(base) == 4 && (base[:3] == "COM" || base[:3] == "LPT") && base[3] >= '1' && base[3] <= '9') {
			return false
		}
		for _, c := range part {
			if c < 32 || c == 127 {
				return false
			}
		}
	}
	return true
}

func inspectSource(root, requested string, required bool) (*boundSource, error) {
	if required && requested == "" {
		return nil, errors.New("--source-sha is required")
	}
	if requested != "" && !sourceSHAPattern.MatchString(requested) {
		return nil, errors.New("invalid source SHA")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := noSymlinkComponents(root, false); err != nil {
		return nil, err
	}
	top, err := sourceGit(root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, errors.New("source is not a Git checkout")
	}
	actualRoot, err := filepath.Abs(strings.TrimSpace(string(top)))
	if err != nil || !samePath(actualRoot, root) {
		return nil, errors.New("source must be the checkout root")
	}
	rawHead, err := sourceGit(root, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return nil, errors.New("source HEAD is unavailable")
	}
	head := strings.TrimSpace(string(rawHead))
	if !sourceSHAPattern.MatchString(head) || (requested != "" && !strings.EqualFold(head, requested)) {
		return nil, errors.New("source SHA does not equal actual checkout HEAD")
	}
	data, err := sourceGit(root, append([]string{"ls-tree", "-r", "-z", head, "--"}, binarySourceRoots...)...).Output()
	if err != nil {
		return nil, errors.New("source HEAD tree unavailable")
	}
	source := &boundSource{head: strings.ToLower(head), files: map[string]boundFile{}}
	records, err := sourceRecords(data)
	if err != nil {
		return nil, err
	}
	folded := map[string]bool{}
	for _, record := range records {
		meta, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if !ok || !sourcePath(name) || len(fields) != 3 || fields[1] != "blob" ||
			(fields[0] != "100644" && fields[0] != "100755") ||
			!sourceSHAPattern.MatchString(fields[2]) || folded[strings.ToLower(name)] {
			return nil, errors.New("unproven source path, type or alias")
		}
		for _, known := range source.order {
			if strings.EqualFold(known, name) {
				return nil, errors.New("source paths have a case-fold alias")
			}
		}
		folded[strings.ToLower(name)] = true
		source.order = append(source.order, name)
		source.files[name] = boundFile{mode: fields[0], oid: fields[2]}
	}
	for _, name := range []string{"go.mod", "go.sum", "cmd/cpa-cloud/main.go", "cmd/cpa-cloud-backup/main.go"} {
		if _, ok := source.files[name]; !ok {
			return nil, errors.New("required source input is absent")
		}
	}
	if err := source.checkIndex(root); err != nil {
		return nil, err
	}
	if err := source.readBlobs(root); err != nil {
		return nil, err
	}
	config, err := sourceGit(root, "config", "--default=false", "--get", "core.autocrlf").Output()
	if err != nil {
		return nil, errors.New("source checkout conversion is unknown")
	}
	switch strings.TrimSpace(strings.ToLower(string(config))) {
	case "true":
		source.crlf = true
	case "false", "input":
	default:
		return nil, errors.New("unsupported source checkout conversion")
	}
	if err := source.checkTree(root, true); err != nil {
		return nil, err
	}
	return source, nil
}
func (source *boundSource) checkIndex(root string) error {
	data, err := sourceGit(root, append([]string{"ls-files", "--stage", "-z", "--"}, binarySourceRoots...)...).Output()
	if err != nil {
		return errors.New("source index unavailable")
	}
	records, err := sourceRecords(data)
	if err != nil || len(records) != len(source.files) {
		return errors.New("source index differs from HEAD")
	}
	seen := map[string]bool{}
	for _, record := range records {
		meta, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		file, exists := source.files[name]
		if !ok || !exists || seen[name] || len(fields) != 3 ||
			fields[2] != "0" || fields[0] != file.mode || fields[1] != file.oid {
			return errors.New("staged or unmerged source input")
		}
		seen[name] = true
	}
	data, err = sourceGit(root, append([]string{"ls-files", "-v", "-z", "--"}, binarySourceRoots...)...).Output()
	if err != nil {
		return errors.New("source index flags unavailable")
	}
	records, err = sourceRecords(data)
	if err != nil || len(records) != len(source.files) {
		return errors.New("source index flag closure mismatch")
	}
	seen = map[string]bool{}
	for _, record := range records {
		if !strings.HasPrefix(record, "H ") {
			return errors.New("hidden source index flag")
		}
		name := record[2:]
		if _, ok := source.files[name]; !ok || seen[name] {
			return errors.New("source index flag closure mismatch")
		}
		seen[name] = true
	}
	for _, view := range [][]string{nil, {"--cached"}, {"--source=" + source.head}} {
		args := append([]string{"check-attr"}, view...)
		args = append(args, "--all", "-z", "--stdin")
		command := sourceGit(root, args...)
		command.Stdin = strings.NewReader(strings.Join(source.order, "\x00") + "\x00")
		data, err := command.Output()
		if err != nil || len(data) != 0 {
			return errors.New("active or unproven source attributes")
		}
	}
	return nil
}

func (source *boundSource) readBlobs(root string) error {
	ids, selected := []string{}, map[string]bool{}
	for _, name := range source.order {
		id := source.files[name].oid
		if !selected[id] {
			ids, selected[id] = append(ids, id), true
		}
	}
	command := sourceGit(root, "cat-file", "--batch")
	command.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	pipe, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return errors.New("raw source blob reader unavailable")
	}
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	reader := bufio.NewReader(pipe)
	values, total := map[string][]byte{}, int64(0)
	for _, id := range ids {
		header, err := reader.ReadString('\n')
		fields := strings.Fields(header)
		if err != nil || len(header) > 128 || len(fields) != 3 || fields[0] != id || fields[1] != "blob" {
			return errors.New("invalid raw source blob header")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > 64<<20 || total+size > 256<<20 {
			return errors.New("source blob exceeds bound")
		}
		total += size
		body := make([]byte, int(size))
		if _, err := io.ReadFull(reader, body); err != nil {
			return errors.New("truncated raw source blob")
		}
		end, err := reader.ReadByte()
		if err != nil || end != '\n' {
			return errors.New("invalid raw source blob delimiter")
		}
		values[id] = body
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		return errors.New("extra raw source blob data")
	}
	waited = true
	if err := command.Wait(); err != nil {
		return errors.New("raw source blob reader failed")
	}
	total = 0
	for _, name := range source.order {
		file := source.files[name]
		file.data = values[file.oid]
		total += int64(len(file.data))
		if total > 256<<20 {
			return errors.New("complete source input size exceeds bound")
		}
		source.files[name] = file
	}
	return nil
}

func lfCheckoutText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, b := range data {
		if (b < 32 && b != '\n' && b != '\t') || b == 127 {
			return false
		}
	}
	return true
}

func (source *boundSource) checkTree(root string, checkout bool) error {
	directories := map[string]bool{"cmd": true, "internal": true}
	for name := range source.files {
		for dir := filepath.ToSlash(filepath.Dir(name)); dir != "."; dir = filepath.ToSlash(filepath.Dir(dir)) {
			directories[dir] = true
		}
	}
	seen := map[string]bool{}
	for _, name := range binarySourceRoots {
		path := filepath.Join(root, name)
		if err := noSymlinkComponents(path, false); err != nil {
			return err
		}
		err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			info, err := entry.Info()
			if err != nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				return errors.New("unproven physical source entry")
			}
			if info.IsDir() {
				if !directories[relative] {
					return errors.New("extra physical source directory")
				}
				return nil
			}
			file, ok := source.files[relative]
			if !ok || !info.Mode().IsRegular() || info.Size() > 128<<20 || seen[relative] {
				return errors.New("extra, nonregular or oversized physical source input")
			}
			if runtime.GOOS != "windows" && (info.Mode().Perm()&0o111 != 0) != (file.mode == "100755") {
				return errors.New("physical source mode differs from HEAD")
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			data, readErr := io.ReadAll(io.LimitReader(f, (128<<20)+1))
			closeErr := f.Close()
			if readErr != nil || closeErr != nil || int64(len(data)) != info.Size() {
				return errors.New("physical source input changed during read")
			}
			equal := bytes.Equal(data, file.data)
			if !equal && checkout && source.crlf && lfCheckoutText(file.data) {
				// Only this exact checkout transport is admitted. The compiler
				// still receives original HEAD bytes, never converted bytes.
				equal = bytes.Equal(data, bytes.ReplaceAll(file.data, []byte("\n"), []byte("\r\n")))
			}
			if !equal {
				return errors.New("physical source bytes differ from unfiltered HEAD")
			}
			seen[relative] = true
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(seen) != len(source.files) {
		return errors.New("missing physical source input")
	}
	return nil
}

type buildSource struct {
	dir       string
	source    *boundSource
	goContext *goBuildContext
}

type goBuildContext struct {
	executable, dir, goRoot, moduleCache, targetOS, targetArch string
	env                                                        []string
}

func (context *goBuildContext) command(args ...string) *exec.Cmd {
	command := exec.Command(context.executable, args...)
	command.Dir = context.dir
	command.Env = append([]string(nil), context.env...)
	return command
}

func newGoBuildContext(dir string, options packageOptions) (*goBuildContext, error) {
	executable, err := exec.LookPath("go")
	if err != nil {
		return nil, errors.New("Go executable unavailable")
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	if err := noSymlinkComponents(executable, false); err != nil {
		return nil, err
	}
	context := &goBuildContext{executable: executable, dir: dir, env: buildEnvironment(options),
		targetOS: options.targetOS, targetArch: options.targetArch}
	data, err := context.command("env", "-json", "GOROOT", "GOMODCACHE", "GOVERSION").Output()
	var actual struct{ GOROOT, GOMODCACHE, GOVERSION string }
	if err != nil || json.Unmarshal(data, &actual) != nil || actual.GOVERSION != runtime.Version() ||
		!filepath.IsAbs(actual.GOROOT) || !filepath.IsAbs(actual.GOMODCACHE) {
		return nil, errors.New("unproven Go toolchain/cache environment")
	}
	for _, path := range []string{actual.GOROOT, actual.GOMODCACHE} {
		if err := noSymlinkComponents(path, false); err != nil {
			return nil, err
		}
	}
	version, err := os.ReadFile(filepath.Join(actual.GOROOT, "VERSION"))
	if err != nil || strings.TrimSpace(strings.SplitN(string(version), "\n", 2)[0]) != runtime.Version() {
		return nil, errors.New("Go SDK version differs from actual compiler")
	}
	context.goRoot, context.moduleCache = actual.GOROOT, actual.GOMODCACHE
	context.env = append(context.env, "GOROOT="+actual.GOROOT, "GOMODCACHE="+actual.GOMODCACHE)
	return context, nil
}

func (build *buildSource) verify() error {
	if err := noSymlinkComponents(build.dir, false); err != nil {
		return err
	}
	entries, err := os.ReadDir(build.dir)
	if err != nil || len(entries) != len(binarySourceRoots) {
		return errors.New("private source root closure mismatch")
	}
	for _, entry := range entries {
		if entry.Name() != "cmd" && entry.Name() != "internal" &&
			entry.Name() != "go.mod" && entry.Name() != "go.sum" {
			return errors.New("extra private source root input")
		}
	}
	return build.source.checkTree(build.dir, false)
}

func buildEnvironment(o packageOptions) []string {
	env := []string{}
	for _, value := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(value, "=", 2)[0])
		switch key {
		case "CGO_ENABLED", "GOOS", "GOARCH", "GOFLAGS", "GOWORK", "GOENV", "GOTOOLCHAIN",
			"GOPROXY", "GOSUMDB", "GO111MODULE", "GOEXPERIMENT", "GOCACHEPROG",
			"GOAUTH", "GONOPROXY", "GONOSUMDB", "GOINSECURE", "GOVCS", "GOROOT", "GOMODCACHE", "GOPATH":
			continue
		}
		env = append(env, value)
	}
	return append(env, "CGO_ENABLED=0", "GOOS="+o.targetOS, "GOARCH="+o.targetArch,
		"GOFLAGS=", "GOWORK=off", "GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off",
		"GOSUMDB=off", "GO111MODULE=on", "GOEXPERIMENT=", "GOCACHEPROG=",
		"GOAUTH=off", "GONOPROXY=none", "GONOSUMDB=none", "GOINSECURE=", "GOVCS=*:off")
}

func (build *buildSource) checkModule() error {
	command := build.goContext.command("mod", "edit", "-json")
	data, err := command.Output()
	if err != nil {
		return errors.New("private source module is invalid")
	}
	var module struct {
		Module           *struct{ Path string }
		Go, Toolchain    string
		Require          []struct{ Path, Version string }
		Replace, Exclude []json.RawMessage
	}
	if json.Unmarshal(data, &module) != nil || module.Module == nil ||
		module.Module.Path != "cpacloud.local/server" || module.Go != "1.26" ||
		(module.Toolchain != "" && module.Toolchain != "go1.26.9") ||
		len(module.Replace) != 0 || len(module.Exclude) != 0 {
		return errors.New("unproven module graph, toolchain or replacement")
	}
	for _, dependency := range module.Require {
		if reviewedGoVersions[dependency.Path] != dependency.Version || dependency.Version == "" {
			return errors.New("unreviewed source dependency")
		}
	}
	command = build.goContext.command("mod", "verify")
	if err := command.Run(); err != nil {
		return errors.New("offline dependency cache verification failed")
	}
	return build.verify()
}

func newBuildSource(root, requested string, required bool, options packageOptions) (*buildSource, error) {
	source, err := inspectSource(root, requested, required)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "cpa-cloud-head-source-")
	if err != nil {
		return nil, err
	}
	build := &buildSource{dir: dir, source: source}
	admitted := false
	defer func() {
		if !admitted {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, name := range source.order {
		file := source.files[name]
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		mode := fs.FileMode(0o644)
		if file.mode == "100755" {
			mode = 0o755
		}
		if err := writeNewFile(path, file.data, mode); err != nil {
			return nil, err
		}
	}
	build.goContext, err = newGoBuildContext(dir, options)
	if err != nil {
		return nil, err
	}
	if err := build.checkModule(); err != nil {
		return nil, err
	}
	admitted = true
	return build, nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

type npmMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	License string `json:"license"`
}

func readNPMMetadata(path string) (npmMetadata, error) {
	var m npmMetadata
	if err := noSymlinkComponents(path, false); err != nil {
		return m, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}
func frontendManifest() string {
	lines := []string{"Browser runtime dependencies and generated runtime helpers included in web/:", "Reviewed bun.lock SHA256 (CRLF normalized to LF): " + reviewedFrontendLockSHA256}
	for _, d := range frontendRuntimeDependencies {
		paths := []string{}
		for _, name := range frontendNoticeFiles(d) {
			paths = append(paths, "THIRD-PARTY-LICENSES/npm/"+safeName(d.Name+"@"+d.Version)+"/"+name)
		}
		lines = append(lines, fmt.Sprintf("- %s@%s | %s | declared license: %s | source: https://registry.npmjs.org/%s/%s | notices: %s", d.Name, d.Version, d.Role, d.License, d.Name, d.Version, strings.Join(paths, ", ")))
	}
	return strings.Join(append(lines, "", "Build-only packages and node_modules are not included in the release archive."), "\n") + "\n"
}
func collectWebAssets(source, output string) error {
	if strings.TrimSpace(output) == "" {
		return errors.New("--output is required")
	}
	root, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	if err := noSymlinkComponents(root, false); err != nil {
		return err
	}
	if err := validateFrontendLock(root); err != nil {
		return err
	}
	out, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if err := noSymlinkComponents(out, true); err != nil {
		return err
	}
	// Check before creating output. Legacy root/release-web remains disjoint
	// from these inputs, while nested dist and input-ancestor paths fail closed.
	for _, name := range []string{"web", "cmd", "internal", "docs", ".git", "go.mod", "go.sum",
		"README.md", "README.en.md", "CONTRIBUTING.md", "AGENTS.md", "THIRD_PARTY_NOTICES.md"} {
		if pathsOverlap(filepath.Join(root, name), out) {
			return errors.New("web asset output overlaps source input")
		}
	}
	out, err = prepareEmptyDirectory(out)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(root, "web", "dist", "index.html")); err != nil {
		return errors.New("web build is missing index.html")
	}
	if err := copyTree(filepath.Join(root, "web", "dist"), filepath.Join(out, "web")); err != nil {
		return err
	}
	for _, d := range frontendRuntimeDependencies {
		dir := filepath.Join(root, "web", "node_modules", filepath.FromSlash(d.Name))
		if err := noSymlinkComponents(dir, false); err != nil {
			return err
		}
		m, err := readNPMMetadata(filepath.Join(dir, "package.json"))
		if err != nil {
			return err
		}
		if m.Name != d.Name || m.Version != d.Version || m.License != d.License {
			return fmt.Errorf("frontend dependency review is stale for %s", d.Name)
		}
		destination := filepath.Join(out, "THIRD-PARTY-LICENSES", "npm", safeName(d.Name+"@"+d.Version))
		for _, name := range frontendNoticeFiles(d) {
			if err := verifyFrontendNotice(d, name, filepath.Join(dir, name)); err != nil {
				return err
			}
			if err := copyNotice(filepath.Join(dir, name), filepath.Join(destination, name)); err != nil {
				return err
			}
		}
		if err := copyNotice(filepath.Join(dir, "package.json"), filepath.Join(destination, "package.json")); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(out, "FRONTEND-DEPENDENCIES.txt"), []byte(frontendManifest()), 0o644); err != nil {
		return err
	}
	files, err := inventoryFiles(out, map[string]bool{})
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "WEB-ASSETS.json"), webAssetManifest{Schema: 1, SourceLockSHA256: reviewedFrontendLockSHA256, Files: files}); err != nil {
		return err
	}
	return validateWebAssets(out)
}

type payloadFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}
type webAssetManifest struct {
	Schema           int           `json:"schema"`
	SourceLockSHA256 string        `json:"source_lock_sha256"`
	Files            []payloadFile `json:"files"`
}

func readJSON(path string, value any) error {
	if err := noSymlinkComponents(path, false); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return errors.New("invalid or oversized manifest")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("trailing or oversized manifest")
	}
	return nil
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeNewFile(path, append(data, '\n'), 0o644)
}
func validateWebAssets(root string) error {
	_, manifest, err := readFrozenWebManifest(root)
	if err != nil {
		return err
	}
	return validateWebAssetsAgainst(root, manifest)
}

func readFrozenWebManifest(root string) ([]byte, webAssetManifest, error) {
	var manifest webAssetManifest
	path := filepath.Join(root, "WEB-ASSETS.json")
	if err := noSymlinkComponents(path, false); err != nil {
		return nil, manifest, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return nil, manifest, errors.New("invalid frozen Web manifest")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, manifest, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) != info.Size() {
		return nil, manifest, errors.New("Web manifest changed during read")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, manifest, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, manifest, errors.New("trailing frozen Web manifest data")
	}
	return data, manifest, nil
}

func validateWebAssetsAgainst(root string, manifest webAssetManifest) error {
	if manifest.Schema != 1 || manifest.SourceLockSHA256 != reviewedFrontendLockSHA256 {
		return errors.New("unknown Web asset manifest schema or source lock")
	}
	files, err := inventoryFiles(root, map[string]bool{"WEB-ASSETS.json": true})
	if err != nil {
		return err
	}
	if err := equalInventory(manifest.Files, files); err != nil {
		return err
	}
	expected := map[string]bool{"FRONTEND-DEPENDENCIES.txt": true}
	for _, d := range frontendRuntimeDependencies {
		base := "THIRD-PARTY-LICENSES/npm/" + safeName(d.Name+"@"+d.Version) + "/"
		expected[base+"package.json"] = true
		m, err := readNPMMetadata(filepath.Join(root, filepath.FromSlash(base), "package.json"))
		if err != nil || m.Name != d.Name || m.Version != d.Version || m.License != d.License {
			return errors.New("invalid frontend dependency identity")
		}
		for _, name := range frontendNoticeFiles(d) {
			expected[base+name] = true
			path := filepath.Join(root, filepath.FromSlash(base+name))
			if info, err := os.Stat(path); err != nil || info.Size() == 0 {
				return errors.New("missing frontend notice text")
			}
			if err := verifyFrontendNotice(d, name, path); err != nil {
				return err
			}
		}
	}
	foundIndex := false
	for _, f := range files {
		if strings.HasPrefix(f.Path, "web/") {
			if f.Path == "web/index.html" && f.Size > 0 {
				foundIndex = true
			}
		} else if expected[f.Path] {
			delete(expected, f.Path)
		} else {
			return errors.New("unexpected Web artifact material")
		}
	}
	if !foundIndex || len(expected) != 0 {
		return errors.New("incomplete Web artifact")
	}
	b, err := os.ReadFile(filepath.Join(root, "FRONTEND-DEPENDENCIES.txt"))
	if err != nil || string(b) != frontendManifest() {
		return errors.New("frontend dependency manifest mismatch")
	}
	return nil
}

type webBuildSource struct {
	dir         string
	rawManifest []byte
	manifest    webAssetManifest
}

func newWebBuildSource(root string) (*webBuildSource, error) {
	return newWebBuildSourceWithCopy(root, copyFile)
}

// A local copy seam gives tests a deterministic phase boundary, never a global
// mutable hook. Production always passes copyFile.
func newWebBuildSourceWithCopy(root string, copier func(string, string, fs.FileMode) error) (*webBuildSource, error) {
	raw, manifest, err := readFrozenWebManifest(root)
	if err != nil {
		return nil, err
	}
	if err := validateWebAssetsAgainst(root, manifest); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "cpa-cloud-frozen-web-")
	if err != nil {
		return nil, err
	}
	source := &webBuildSource{dir: dir, rawManifest: raw, manifest: manifest}
	admitted := false
	defer func() {
		if !admitted {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, row := range manifest.Files {
		if err := copier(filepath.Join(root, filepath.FromSlash(row.Path)),
			filepath.Join(dir, filepath.FromSlash(row.Path)), 0o644); err != nil {
			return nil, err
		}
	}
	if err := writeNewFile(filepath.Join(dir, "WEB-ASSETS.json"), raw, 0o644); err != nil {
		return nil, err
	}
	if err := source.verifyAt(root); err != nil {
		return nil, err
	}
	if err := source.verifyAt(dir); err != nil {
		return nil, err
	}
	admitted = true
	return source, nil
}

func (source *webBuildSource) verifyAt(root string) error {
	raw, _, err := readFrozenWebManifest(root)
	if err != nil || !bytes.Equal(raw, source.rawManifest) {
		return errors.New("Web manifest differs from initially validated bytes")
	}
	return validateWebAssetsAgainst(root, source.manifest)
}

func (source *webBuildSource) verifyStage(stage string) error {
	raw, _, err := readFrozenWebManifest(stage)
	if err != nil || !bytes.Equal(raw, source.rawManifest) {
		return errors.New("staged Web manifest differs from initial bytes")
	}
	files, err := inventoryFiles(stage, map[string]bool{"WEB-ASSETS.json": true})
	if err != nil {
		return err
	}
	selected := []payloadFile{}
	for _, row := range files {
		if strings.HasPrefix(row.Path, "web/") || strings.HasPrefix(row.Path, "THIRD-PARTY-LICENSES/npm/") ||
			row.Path == "FRONTEND-DEPENDENCIES.txt" {
			selected = append(selected, row)
		}
	}
	return equalInventory(source.manifest.Files, selected)
}

func (source *webBuildSource) copyToStage(stage string) error {
	return source.copyToStageWith(stage, copyFile)
}

func (source *webBuildSource) copyToStageWith(stage string, copier func(string, string, fs.FileMode) error) error {
	if err := source.verifyAt(source.dir); err != nil {
		return err
	}
	for _, row := range source.manifest.Files {
		if err := copier(filepath.Join(source.dir, filepath.FromSlash(row.Path)),
			filepath.Join(stage, filepath.FromSlash(row.Path)), 0o644); err != nil {
			return err
		}
	}
	if err := writeNewFile(filepath.Join(stage, "WEB-ASSETS.json"), source.rawManifest, 0o644); err != nil {
		return err
	}
	if err := source.verifyAt(source.dir); err != nil {
		return err
	}
	return source.verifyStage(stage)
}

func equalInventory(expected, actual []payloadFile) error {
	if len(expected) != len(actual) {
		return errors.New("file closure count mismatch")
	}
	seen := map[string]bool{}
	for i, f := range expected {
		if err := validatePayloadPath(f.Path); err != nil {
			return err
		}
		key := strings.ToLower(f.Path)
		if seen[key] {
			return errors.New("duplicate manifest path")
		}
		seen[key] = true
		if f != actual[i] {
			return errors.New("file closure content mismatch")
		}
	}
	return nil
}

type binaryArtifact struct {
	Name      string `json:"name"`
	Package   string `json:"package"`
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	HelpCheck string `json:"help_check"`
	Path      string `json:"-"`
}
type binaryBuildInfo struct {
	Name string
	Info *rdebug.BuildInfo
}
type linkedModule struct {
	Path     string   `json:"path"`
	Version  string   `json:"version"`
	Binaries []string `json:"binaries"`
	Notices  []string `json:"notices"`
}

func reviewBuildInfos(inputs []binaryBuildInfo, o packageOptions) ([]linkedModule, error) {
	expected := map[string]bool{"cpa-cloud": true}
	if o.includeBackupCLI {
		expected["cpa-cloud-backup"] = true
	}
	modules := map[string]*linkedModule{}
	for _, input := range inputs {
		info := input.Info
		if !expected[input.Name] || info == nil {
			return nil, errors.New("unknown or duplicate binary")
		}
		delete(expected, input.Name)
		if info.Path != "cpacloud.local/server/cmd/"+input.Name || info.Main.Path != "cpacloud.local/server" || info.Main.Replace != nil || info.GoVersion != runtime.Version() {
			return nil, errors.New("unreviewed binary build identity or toolchain")
		}
		settings := map[string]string{}
		for _, s := range info.Settings {
			if _, ok := settings[s.Key]; ok {
				return nil, errors.New("duplicate Go build setting")
			}
			settings[s.Key] = s.Value
		}
		if settings["GOOS"] != o.targetOS || settings["GOARCH"] != o.targetArch || settings["CGO_ENABLED"] != "0" || (settings["-ldflags"] != "-X main.version="+o.version && !(settings["-ldflags"] == "" && settings["-trimpath"] == "true")) {
			return nil, errors.New("binary target or embedded version mismatch")
		}
		seen := map[string]bool{}
		for _, d := range info.Deps {
			if d == nil || seen[d.Path] {
				return nil, errors.New("duplicate or nil linked module")
			}
			seen[d.Path] = true
			version, reviewed := reviewedGoVersions[d.Path]
			files, noticesReviewed := goNoticeFiles[d.Path]
			if !reviewed || !noticesReviewed || len(files) == 0 || version != d.Version || d.Replace != nil {
				return nil, errors.New("unreviewed linked module/version/replacement")
			}
			m, ok := modules[d.Path]
			if !ok {
				m = &linkedModule{Path: d.Path, Version: d.Version, Notices: append([]string(nil), files...)}
				modules[d.Path] = m
			}
			if m.Version != d.Version {
				return nil, errors.New("conflicting linked module versions")
			}
			m.Binaries = append(m.Binaries, input.Name)
		}
	}
	if len(expected) != 0 {
		return nil, errors.New("required binary build information missing")
	}
	result := make([]linkedModule, 0, len(modules))
	for _, m := range modules {
		sort.Strings(m.Binaries)
		result = append(result, *m)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}
func collectBinaryGoNotices(source *buildSource, binaries []binaryArtifact, o packageOptions, noticeRoot string) ([]linkedModule, string, error) {
	if source.goContext == nil || source.goContext.targetOS != o.targetOS || source.goContext.targetArch != o.targetArch {
		return nil, "", errors.New("notice context differs from compiler context")
	}
	inputs := make([]binaryBuildInfo, 0, len(binaries))
	for _, b := range binaries {
		info, err := buildinfo.ReadFile(b.Path)
		if err != nil {
			return nil, "", err
		}
		inputs = append(inputs, binaryBuildInfo{Name: b.Name, Info: info})
	}
	modules, err := reviewBuildInfos(inputs, o)
	if err != nil {
		return nil, "", err
	}
	goRoot := source.goContext.goRoot
	for _, name := range []string{"LICENSE", "PATENTS", "VERSION"} {
		if err := copyNotice(filepath.Join(goRoot, name), filepath.Join(noticeRoot, "go-runtime", name)); err != nil {
			return nil, "", err
		}
	}
	lines := []string{"Go runtime: " + runtime.Version(), "Actual per-binary linked module union (standard library notices in go-runtime/):"}
	for _, m := range modules {
		dir, err := source.goContext.moduleDirectory(m.Path, m.Version)
		if err != nil {
			return nil, "", err
		}
		destination := filepath.Join(noticeRoot, "go-modules", safeName(m.Path+"@"+m.Version))
		for _, name := range m.Notices {
			if err := validateRelative(name); err != nil {
				return nil, "", err
			}
			if err := copyNotice(filepath.Join(dir, filepath.FromSlash(name)), filepath.Join(destination, filepath.FromSlash(name))); err != nil {
				return nil, "", err
			}
		}
		lines = append(lines, fmt.Sprintf("- %s@%s | linked by: %s | notices: THIRD-PARTY-LICENSES/go-modules/%s", m.Path, m.Version, strings.Join(m.Binaries, ", "), safeName(m.Path+"@"+m.Version)))
	}
	return modules, strings.Join(lines, "\n") + "\n", nil
}
func copyNotice(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("missing/nonregular/empty required notice")
	}
	return copyFile(source, destination, 0o644)
}

func buildPackage(o packageOptions) error {
	if err := validatePackageOptions(o); err != nil {
		return err
	}
	if strings.TrimSpace(o.webAssets) == "" || strings.TrimSpace(o.output) == "" {
		return errors.New("--web-assets and --output are required")
	}
	root, err := filepath.Abs(o.source)
	if err != nil {
		return err
	}
	source, err := newBuildSource(root, o.sourceSHA, o.version == "v0.1.0", o)
	if err != nil {
		return err
	}
	defer os.RemoveAll(source.dir)
	sha := source.source.head
	webRoot, err := filepath.Abs(o.webAssets)
	if err != nil {
		return err
	}
	web, err := newWebBuildSource(webRoot)
	if err != nil {
		return err
	}
	defer os.RemoveAll(web.dir)
	out, err := filepath.Abs(o.output)
	if err != nil {
		return err
	}
	if pathsOverlap(root, out) || pathsOverlap(webRoot, out) {
		return errors.New("release output overlaps input")
	}
	baseName := fmt.Sprintf("cpa-cloud_%s_%s_%s", o.version, o.assetOS, o.targetArch)
	extension := ".tar.gz"
	if o.targetOS == "windows" {
		extension = ".zip"
	}
	archivePath := filepath.Join(out, baseName+extension)
	for _, path := range []string{archivePath, archivePath + ".sha256"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("refusing to overwrite existing release output")
		}
	}
	if _, err := prepareOutputDirectory(out); err != nil {
		return err
	}
	parent, err := os.MkdirTemp("", "cpa-cloud-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(parent)
	stage := filepath.Join(parent, baseName)
	if err := os.Mkdir(stage, 0o755); err != nil {
		return err
	}
	names := []string{"cpa-cloud"}
	if o.includeBackupCLI {
		names = append(names, "cpa-cloud-backup")
	}
	binaries := make([]binaryArtifact, 0, len(names))
	for _, name := range names {
		fileName := name
		if o.targetOS == "windows" {
			fileName += ".exe"
		}
		path := filepath.Join(stage, fileName)
		if err := buildBinary(source, path, "./cmd/"+name, o); err != nil {
			return err
		}
		if o.version == "v0.1.0" {
			if err := verifyELFVersion(path, o.version); err != nil {
				return err
			}
		}
		helpCheck := "not executed: cross-compiled target"
		if runtime.GOOS == o.targetOS && runtime.GOARCH == o.targetArch {
			args := []string{"--help"}
			if name == "cpa-cloud-backup" {
				args = []string{"verify", "--help"}
			}
			command := exec.Command(path, args...)
			command.Stdout = io.Discard
			command.Stderr = io.Discard
			if err := command.Run(); err != nil {
				return errors.New("native help execution check failed")
			}
			helpCheck = "passed (help only, no service or data operation)"
		}
		digest, err := hashFile(path)
		if err != nil {
			return err
		}
		binaries = append(binaries, binaryArtifact{Name: name, Path: path, Package: "./cmd/" + name, Version: o.version, SHA256: digest, HelpCheck: helpCheck})
	}
	if actual, err := verifySource(root, sha, true); err != nil || actual != sha {
		return errors.New("source changed during binary builds")
	}
	if err := web.verifyAt(web.dir); err != nil {
		return err
	}
	if err := web.copyToStage(stage); err != nil {
		return err
	}
	for _, name := range []string{"README.md", "README.en.md", "CONTRIBUTING.md", "AGENTS.md", "THIRD_PARTY_NOTICES.md"} {
		if err := copyFile(filepath.Join(root, name), filepath.Join(stage, name), 0o644); err != nil {
			return err
		}
	}
	if err := copyTrackedDirectory(root, "docs", filepath.Join(stage, "docs")); err != nil {
		return err
	}
	modules, goManifest, err := collectBinaryGoNotices(source, binaries, o, filepath.Join(stage, "THIRD-PARTY-LICENSES"))
	if err != nil {
		return err
	}
	if err := writeNewFile(filepath.Join(stage, "GO-DEPENDENCIES.txt"), []byte(goManifest), 0o644); err != nil {
		return err
	}
	if err := writeNewFile(filepath.Join(stage, "VERSION"), []byte(o.version+"\n"), 0o644); err != nil {
		return err
	}
	info := fmt.Sprintf("Version: %s\nCommit: %s\nTarget: %s/%s\nPackage host: %s/%s\nGo toolchain: %s\nSource: actual Git checkout HEAD; binary input paths clean before/after build; GITHUB_SHA ignored.\nScope: package verification only; no target deployment, restore, production-readiness or full-CI claim.\n", o.version, sha, o.targetOS, o.targetArch, runtime.GOOS, runtime.GOARCH, runtime.Version())
	info += "Binary compiler input: one shared private snapshot of unfiltered fixed HEAD blobs.\n"
	for _, b := range binaries {
		info += b.Name + " help: " + b.HelpCheck + "\n"
	}
	if err := writeNewFile(filepath.Join(stage, "BUILD-INFO.txt"), []byte(info), 0o644); err != nil {
		return err
	}
	if err := web.verifyStage(stage); err != nil {
		return err
	}
	if err := writeReleaseManifest(stage, o, sha, binaries, modules); err != nil {
		return err
	}
	if err := writeTreeChecksums(stage); err != nil {
		return err
	}
	if err := verifyTreeChecksums(stage); err != nil {
		return err
	}
	if o.targetOS == "windows" {
		err = writeZip(archivePath, parent, baseName)
	} else {
		err = writeTarGz(archivePath, parent, baseName)
	}
	if err != nil {
		return err
	}
	digest, err := hashFile(archivePath)
	if err != nil {
		return err
	}
	if err := writeNewFile(archivePath+".sha256", []byte(digest+"  "+filepath.Base(archivePath)+"\n"), 0o644); err != nil {
		return err
	}
	return removeTemporaryTree(parent)
}
func buildBinary(source *buildSource, destination, pkg string, o packageOptions) error {
	if pkg != "./cmd/cpa-cloud" && pkg != "./cmd/cpa-cloud-backup" {
		return errors.New("unreviewed binary package")
	}
	if err := source.verify(); err != nil {
		return err
	}
	if source.goContext == nil || source.goContext.targetOS != o.targetOS || source.goContext.targetArch != o.targetArch {
		return errors.New("binary context differs from fixed compiler context")
	}
	command := source.goContext.command("build", "-mod=readonly", "-buildvcs=false", "-trimpath", "-ldflags", "-X main.version="+o.version, "-o", destination, pkg)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("build %s: %w", pkg, err)
	}
	if err := source.verify(); err != nil {
		return err
	}
	if o.targetOS != "windows" {
		return os.Chmod(destination, 0o755)
	}
	return nil
}
func pathsOverlap(a, b string) bool {
	within := func(root, path string) bool {
		rel, err := filepath.Rel(root, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
	}
	return within(a, b) || within(b, a)
}

type releaseManifest struct {
	Schema    int              `json:"schema"`
	Version   string           `json:"version"`
	SourceSHA string           `json:"source_sha"`
	Target    string           `json:"target"`
	Binaries  []binaryArtifact `json:"binaries"`
	GoModules []linkedModule   `json:"go_modules"`
	Files     []payloadFile    `json:"files"`
	Closure   string           `json:"closure"`
}

func writeReleaseManifest(root string, o packageOptions, sha string, binaries []binaryArtifact, modules []linkedModule) error {
	files, err := inventoryFiles(root, map[string]bool{"MANIFEST.json": true, "SHA256SUMS.txt": true})
	if err != nil {
		return err
	}
	value := releaseManifest{Schema: 1, Version: o.version, SourceSHA: sha, Target: o.targetOS + "/" + o.targetArch, Binaries: binaries, GoModules: modules, Files: files,
		Closure: "files contains every ordinary payload file except root MANIFEST.json and root SHA256SUMS.txt; SHA256SUMS.txt additionally hashes MANIFEST.json; archive sidecar hashes the complete archive"}
	return writeJSON(filepath.Join(root, "MANIFEST.json"), value)
}
func payloadMode(relative string) fs.FileMode {
	switch relative {
	case "cpa-cloud", "cpa-cloud-backup", "cpa-cloud.exe", "cpa-cloud-backup.exe":
		return 0o755
	default:
		return 0o644
	}
}
func validateEntryMode(mode fs.FileMode) error {
	if !mode.IsRegular() && !mode.IsDir() {
		return errors.New("nonregular release entry")
	}
	return nil
}
func validateRelative(path string) error {
	if path == "" || strings.ContainsAny(path, "\\:\x00\r\n") || strings.HasPrefix(path, "/") {
		return errors.New("unsafe release path")
	}
	for _, c := range path {
		if c < 32 || c == 127 {
			return errors.New("control character in release path")
		}
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("release path is not canonical relative")
		}
	}
	return nil
}
func validatePayloadPath(path string) error {
	if err := validateRelative(path); err != nil {
		return err
	}
	for _, part := range strings.Split(strings.ToLower(path), "/") {
		switch part {
		case ".git", "node_modules", "data", "secrets", "sessions", "session", "keys", "key", "backup", "backups", "credentials", "config", ".env", "config.json", "master.key":
			return errors.New("private/build material is outside release scope")
		}
		if strings.HasPrefix(part, ".env") || strings.HasSuffix(part, ".db") || strings.HasSuffix(part, ".sqlite") || strings.HasSuffix(part, ".key") || strings.HasSuffix(part, ".pem") {
			return errors.New("data or key material is outside release scope")
		}
	}
	return nil
}
func inventoryFiles(root string, excluded map[string]bool) ([]payloadFile, error) {
	if err := noSymlinkComponents(root, false); err != nil {
		return nil, err
	}
	files := []payloadFile{}
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := validateEntryMode(entry.Type()); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			if !entry.IsDir() {
				return errors.New("release input must be a directory")
			}
			return nil
		}
		relative = filepath.ToSlash(relative)
		if err := validatePayloadPath(relative); err != nil {
			return err
		}
		key := strings.ToLower(relative)
		if seen[key] {
			return errors.New("duplicate release path")
		}
		seen[key] = true
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("nonregular release material")
		}
		if excluded[relative] {
			return nil
		}
		digest, err := hashFile(path)
		if err != nil {
			return err
		}
		files = append(files, payloadFile{Path: relative, Size: info.Size(), SHA256: digest, Mode: uint32(payloadMode(relative))})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}
func writeNewFile(path string, data []byte, mode fs.FileMode) error {
	if err := noSymlinkComponents(filepath.Dir(path), true); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
func noSymlinkComponents(path string, allowMissing bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		info, err := os.Lstat(absolute)
		if err != nil {
			if !allowMissing || !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink component in release path")
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			break
		}
		absolute = parent
	}
	return nil
}

func verifyELFVersion(path, version string) error {
	f, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_X86_64 {
		return errors.New("stable binary must be 64-bit x86 ELF")
	}
	symbols, err := f.Symbols()
	if err != nil {
		return err
	}
	read := func(address, size uint64) ([]byte, error) {
		if size == 0 || size > 256 || address+size < address {
			return nil, errors.New("invalid embedded version bounds")
		}
		for _, segment := range f.Progs {
			if segment.Type != elf.PT_LOAD || address < segment.Vaddr {
				continue
			}
			offset := address - segment.Vaddr
			if offset > segment.Filesz || size > segment.Filesz-offset {
				continue
			}
			b := make([]byte, int(size))
			_, err := segment.ReadAt(b, int64(offset))
			return b, err
		}
		return nil, errors.New("embedded version is outside file-backed ELF segments")
	}
	found := 0
	for _, symbol := range symbols {
		if symbol.Name != "main.version" {
			continue
		}
		found++
		data, err := read(symbol.Value, 16)
		if err != nil {
			return err
		}
		size := f.ByteOrder.Uint64(data[8:])
		if size != uint64(len(version)) {
			return errors.New("embedded binary version length mismatch")
		}
		value, err := read(f.ByteOrder.Uint64(data[:8]), size)
		if err != nil || string(value) != version {
			return errors.New("embedded binary version mismatch")
		}
	}
	if found != 1 {
		return errors.New("binary version symbol is missing or duplicated")
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

func (context *goBuildContext) moduleDirectory(path, version string) (string, error) {
	if reviewedGoVersions[path] != version || version == "" {
		return "", errors.New("unreviewed module cache identity")
	}
	candidate := filepath.Join(context.moduleCache, filepath.FromSlash(path+"@"+version))
	if err := noSymlinkComponents(candidate, false); err != nil {
		return "", err
	}
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "", errors.New("required module absent from offline cache")
	}
	return candidate, nil
}
func prepareEmptyDirectory(path string) (string, error) {
	out, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := noSymlinkComponents(out, true); err != nil {
		return "", err
	}
	if err := os.Mkdir(out, 0o755); err != nil {
		return "", err
	}
	return out, nil
}
func prepareOutputDirectory(path string) (string, error) {
	out, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := noSymlinkComponents(out, true); err != nil {
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
	if err := noSymlinkComponents(root, false); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("copy tree source is not a directory")
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("release input must not contain symlinks")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative != "." {
			if err := validatePayloadPath(filepath.ToSlash(relative)); err != nil {
				return err
			}
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			if err := noSymlinkComponents(target, true); err != nil {
				return err
			}
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
	if err := noSymlinkComponents(source, false); err != nil {
		return err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("release input must be a regular file")
	}
	if err := noSymlinkComponents(destination, true); err != nil {
		return err
	}
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
		_ = os.Remove(destination)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return closeErr
	}
	return nil
}
func copyTrackedDirectory(root, repositoryPath, destination string) error {
	if err := validateRelative(repositoryPath); err != nil {
		return err
	}
	command := exec.Command("git", "ls-files", "-z", "--", repositoryPath)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	copied := 0
	for _, relative := range strings.Split(string(output), "\x00") {
		if relative == "" {
			continue
		}
		if err := validatePayloadPath(relative); err != nil {
			return err
		}
		if !strings.HasPrefix(relative, repositoryPath+"/") {
			return errors.New("tracked path escaped documentation root")
		}
		key := strings.ToLower(relative)
		if seen[key] {
			return errors.New("duplicate tracked documentation")
		}
		seen[key] = true
		within := strings.TrimPrefix(relative, repositoryPath+"/")
		if err := copyFile(filepath.Join(root, filepath.FromSlash(relative)), filepath.Join(destination, filepath.FromSlash(within)), 0o644); err != nil {
			return err
		}
		copied++
	}
	if copied == 0 {
		return errors.New("no tracked documentation files found")
	}
	return nil
}
func checksumText(files []payloadFile) string {
	var text strings.Builder
	for _, f := range files {
		fmt.Fprintf(&text, "%s  %s\n", f.SHA256, f.Path)
	}
	return text.String()
}
func writeTreeChecksums(root string) error {
	files, err := inventoryFiles(root, map[string]bool{"SHA256SUMS.txt": true})
	if err != nil {
		return err
	}
	return writeNewFile(filepath.Join(root, "SHA256SUMS.txt"), []byte(checksumText(files)), 0o644)
}
func verifyTreeChecksums(root string) error {
	files, err := inventoryFiles(root, map[string]bool{"SHA256SUMS.txt": true})
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(root, "SHA256SUMS.txt"))
	if err != nil {
		return err
	}
	if string(b) != checksumText(files) {
		return errors.New("checksum file is not the exact ordinary-file closure")
	}
	if _, err := os.Lstat(filepath.Join(root, "MANIFEST.json")); err == nil {
		var manifest releaseManifest
		if err := readJSON(filepath.Join(root, "MANIFEST.json"), &manifest); err != nil {
			return err
		}
		if manifest.Schema != 1 || !sourceSHAPattern.MatchString(manifest.SourceSHA) || validateReleaseTag(manifest.Version) != nil {
			return errors.New("invalid release manifest identity")
		}
		payload, err := inventoryFiles(root, map[string]bool{"MANIFEST.json": true, "SHA256SUMS.txt": true})
		if err != nil {
			return err
		}
		return equalInventory(manifest.Files, payload)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func hashFile(path string) (string, error) {
	if err := noSymlinkComponents(path, false); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("cannot hash nonregular release material")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
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
			header.SetMode(os.ModeDir | 0o755)
		} else {
			header.SetMode(payloadMode(filepath.ToSlash(relative)))
		}
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
		if info.IsDir() {
			header.Mode = 0o755
		} else {
			header.Mode = int64(payloadMode(filepath.ToSlash(relative)))
		}
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
		if err := validateEntryMode(info.Mode()); err != nil {
			return err
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
