package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutIsInsideRoot(t *testing.T) {
	p := FromRoot("/tmp/x")
	checks := map[string]string{
		p.DB():         "/tmp/x/clipnest.db",
		p.Settings():   "/tmp/x/settings.json",
		p.SyncConfig(): "/tmp/x/sync.json",
		p.BackupsDir(): "/tmp/x/backups",
		p.Backup("a.cnbak"): "/tmp/x/backups/a.cnbak",
	}
	for got, want := range checks {
		if got != want {
			t.Fatalf("得 %q，期望 %q", got, want)
		}
	}
	hash := strings.Repeat("ab", 32)
	if got, want := p.ImageOrigin(hash), "/tmp/x/images/origin/ab/"+hash+".png"; got != want {
		t.Fatalf("原图路径 %q", got)
	}
	if got, want := p.ImageThumbnail(hash), "/tmp/x/images/thumbnails/ab/"+hash+".png"; got != want {
		t.Fatalf("缩略图路径 %q", got)
	}
	if imageName("cd") != "cd.png" {
		t.Fatalf("imageName=%q", imageName("cd"))
	}
}

func TestEnsureCreatesAllDirsAndIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	p := FromRoot(root)
	if _, err := os.Stat(root); err == nil {
		t.Fatal("还没建就存在")
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		root,
		filepath.Join(root, "images", "origin"),
		filepath.Join(root, "images", "thumbnails"),
		filepath.Join(root, "backups"),
	} {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Fatalf("缺目录 %s", dir)
		}
	}

	hash := strings.Repeat("ff", 32)
	origin, thumb, err := p.EnsureImageDirs(hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{origin, thumb} {
		if fi, err := os.Stat(filepath.Dir(f)); err != nil || !fi.IsDir() {
			t.Fatalf("分片目录没建：%s", f)
		}
	}
	if !strings.Contains(origin, filepath.Join("images", "origin", "ff")) {
		t.Fatalf("分片目录不对：%s", origin)
	}
}

func TestImageNameAcceptsOddHashes(t *testing.T) {
	p := FromRoot("/tmp/y")
	// 不足 2 字符退到 00 分片，不 panic
	if !strings.HasSuffix(p.ImageOrigin("a"), filepath.Join("origin", "00", "a.png")) {
		t.Fatalf("得 %q", p.ImageOrigin("a"))
	}
	if !strings.HasSuffix(p.ImageOrigin(""), filepath.Join("origin", "00", ".png")) {
		t.Fatalf("得 %q", p.ImageOrigin(""))
	}
}

func TestPlatformRootFollowsEachOS(t *testing.T) {
	home := "/Users/me"
	appdata := `C:\Users\me\AppData\Roaming`
	xdg := "/home/me/.local/share"

	if got, want := platformRoot("darwin", "", "/x", home), filepath.Join("/Users/me", "Library", "Application Support", AppDirName); got != want {
		t.Fatalf("macOS 得 %q", got)
	}
	if got, want := platformRoot("windows", appdata, "", home), filepath.Join(appdata, AppDirName); got != want {
		t.Fatalf("Windows 得 %q", got)
	}
	if got, want := platformRoot("linux", "", xdg, home), filepath.Join(xdg, AppDirName); got != want {
		t.Fatalf("Linux 得 %q", got)
	}
	// 什么都没有也别 panic
	if !strings.HasSuffix(platformRoot("linux", "", "", ""), AppDirName) {
		t.Fatal("兜底路径不对")
	}
}

func TestPlatformMatchesThisOS(t *testing.T) {
	p := Platform()
	if !strings.HasSuffix(p.Root(), AppDirName) {
		t.Fatalf("根目录 %q", p.Root())
	}
}
