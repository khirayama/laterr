package paths

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnsureDirsRepairsPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	p := Paths{Dir: dir, Logs: filepath.Join(dir, "logs")}
	for _, d := range []string{p.Dir, p.Logs} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p.Dir, p.Logs} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v, want 0700", d, fi.Mode().Perm())
		}
	}
}

func TestEnsureDirsRejectsSymlinks(t *testing.T) {
	for _, component := range []string{"data", "logs"} {
		t.Run(component, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			p := Paths{Dir: filepath.Join(root, "data"), Logs: filepath.Join(root, "data", "logs")}
			link := p.Dir
			if component == "logs" {
				if err := os.Mkdir(p.Dir, 0o700); err != nil {
					t.Fatal(err)
				}
				link = p.Logs
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if err := p.EnsureDirs(); err == nil {
				t.Fatal("accepted a symlink")
			}
			if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o755 {
				t.Fatalf("symlink target permissions changed: %v, %v", fi, err)
			}
		})
	}
}

func TestOpenPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(path, []byte("existing secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := OpenPrivateFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(" preserved"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file permissions: %v, %v", fi, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "existing secret preserved" {
		t.Fatalf("file contents: %q, %v", data, err)
	}
}

func TestOpenPrivateFileRejectsUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	link, fifo := filepath.Join(dir, "link"), filepath.Join(dir, "fifo")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, fifo, dir} {
		if f, err := OpenPrivateFile(path, os.O_RDWR|os.O_CREATE); err == nil {
			f.Close()
			t.Errorf("accepted unsafe path: %s", path)
		}
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "untouched" {
		t.Fatalf("symlink target was modified: %q, %v", data, err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("symlink target permissions changed: %v, %v", fi, err)
	}
}
