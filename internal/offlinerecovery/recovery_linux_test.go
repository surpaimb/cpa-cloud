//go:build linux

package offlinerecovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOfflinePinsRejectNamespaceAndContentChanges(t *testing.T) {
	root := t.TempDir()
	if os.Chmod(root, 0o700) != nil {
		t.Fatal("setup")
	}
	path := filepath.Join(root, "binding")
	if os.WriteFile(path, []byte("public-fixture"), 0o600) != nil {
		t.Fatal("setup")
	}
	p, err := pin(path, false)
	if err != nil {
		t.Fatal("pin")
	}
	defer p.file.Close()
	if _, err := p.read(4096); err != nil {
		t.Fatal("read")
	}
	if os.WriteFile(path, []byte("public-changed"), 0o600) != nil || p.check(false) == nil {
		t.Fatal("in-place mutation admitted")
	}
	if os.Remove(path) != nil || os.Symlink("/dev/null", path) != nil {
		t.Fatal("setup")
	}
	if _, err := pin(path, false); err == nil {
		t.Fatal("link admitted")
	}
	if os.Remove(path) != nil || os.WriteFile(path, []byte("public-fixture"), 0o644) != nil {
		t.Fatal("setup")
	}
	if _, err := pin(path, false); err == nil {
		t.Fatal("unsafe mode admitted")
	}
}

func TestOfflineTargetParentReplacement(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "parent")
	if os.Mkdir(parentPath, 0o700) != nil {
		t.Fatal("setup")
	}
	p, err := pin(parentPath, true)
	if err != nil {
		t.Fatal("pin")
	}
	defer p.file.Close()
	target := filepath.Join(parentPath, "restored")
	if targetAbsent(p, target) != nil {
		t.Fatal("new target")
	}
	if os.Rename(parentPath, parentPath+"-before") != nil || os.Mkdir(parentPath, 0o700) != nil {
		t.Fatal("setup")
	}
	if targetAbsent(p, target) == nil {
		t.Fatal("replacement admitted")
	}
}

func TestOfflinePinsRejectLinksAtEveryPathLevel(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	link := filepath.Join(root, "alias")
	if os.Mkdir(actual, 0o700) != nil || os.Symlink(actual, link) != nil {
		t.Fatal("setup")
	}
	if _, err := pin(link, true); err == nil {
		t.Fatal("directory link admitted")
	}
	if os.WriteFile(filepath.Join(actual, "file"), []byte("fixture"), 0o600) != nil {
		t.Fatal("setup")
	}
	if _, err := pin(filepath.Join(link, "file"), false); err == nil {
		t.Fatal("ancestor link admitted")
	}
}

func TestOfflinePinsRejectHardlinksAndForeignOwner(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if os.WriteFile(path, []byte("public-fixture"), 0o600) != nil {
		t.Fatal("setup")
	}
	if os.Link(path, filepath.Join(root, "alias")) != nil {
		t.Fatal("setup")
	}
	if _, err := pin(path, false); err == nil {
		t.Fatal("hardlink admitted")
	}
	if os.Remove(filepath.Join(root, "alias")) != nil {
		t.Fatal("setup")
	}
	if os.Geteuid() != 0 {
		t.Skip("foreign owner mutation requires native root fixture")
	}
	if os.Chown(path, 65534, 65534) != nil {
		t.Fatal("setup")
	}
	defer os.Chown(path, 0, 0)
	if _, err := pin(path, false); err == nil {
		t.Fatal("foreign owner admitted")
	}
}
