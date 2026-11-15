// Package paths 是数据目录与资源布局。
//
// 一个根目录装下全部可写状态：
//
//	<root>/
//	  clipnest.db
//	  images/origin/<hh>/<hash>.png
//	  images/thumbnails/<hh>/<hash>.png
//	  settings.json
//	  sync.json
//	  backups/
//
// <root> 在 macOS 是 ~/Library/Application Support/clipnest，Windows 是
// %APPDATA%\clipnest，Linux 是 $XDG_DATA_HOME/clipnest；环境变量
// CLIPNEST_DATA_DIR 可整体覆盖（调试与测试用）。
//
// 不用框架的 app data 目录：它依赖运行时，测试里拿不到。
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// 根目录下的固定文件名。
const (
	DBFile       = "clipnest.db"
	SettingsFile = "settings.json"
	SyncFile     = "sync.json"
)

// AppDirName 是各平台默认根目录的最后一级。
const AppDirName = "clipnest"

// Paths 是应用全部可写状态的根。
type Paths struct {
	root string
}

// FromRoot 用给定根目录（不做平台推断、不创建目录）。
func FromRoot(root string) Paths { return Paths{root: root} }

// Platform 按平台推断根目录；CLIPNEST_DATA_DIR 优先。
func Platform() Paths {
	if dir := os.Getenv("CLIPNEST_DATA_DIR"); dir != "" {
		return FromRoot(dir)
	}
	return FromRoot(platformRoot(runtime.GOOS, os.Getenv("APPDATA"), os.Getenv("XDG_DATA_HOME"), os.Getenv("HOME")))
}

// Root 是根目录。
func (p Paths) Root() string { return p.root }

// DB 是数据库文件路径。
func (p Paths) DB() string { return filepath.Join(p.root, DBFile) }

// Settings 是设置文件路径。
func (p Paths) Settings() string { return filepath.Join(p.root, SettingsFile) }

// SyncConfig 是同步配置文件路径。
func (p Paths) SyncConfig() string { return filepath.Join(p.root, SyncFile) }

// BackupsDir 是备份目录。
func (p Paths) BackupsDir() string { return filepath.Join(p.root, "backups") }

// Backup 是 backups/<name>；name 由调用方给（建议带时间戳）。
func (p Paths) Backup(name string) string { return filepath.Join(p.BackupsDir(), name) }

// ImagesDir 是图片总目录。
func (p Paths) ImagesDir() string { return filepath.Join(p.root, "images") }

// ImageOrigin 是原图路径：images/origin/<hh>/<hash>.png。
func (p Paths) ImageOrigin(hash string) string {
	return filepath.Join(p.ImagesDir(), "origin", shard(hash), imageName(hash))
}

// ImageThumbnail 是缩略图路径：images/thumbnails/<hh>/<hash>.png。
func (p Paths) ImageThumbnail(hash string) string {
	return filepath.Join(p.ImagesDir(), "thumbnails", shard(hash), imageName(hash))
}

// Ensure 建好全部目录（幂等）。
func (p Paths) Ensure() error {
	for _, dir := range []string{
		p.root,
		filepath.Join(p.ImagesDir(), "origin"),
		filepath.Join(p.ImagesDir(), "thumbnails"),
		p.BackupsDir(),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// EnsureImageDirs 建好某条图片条目的分片目录，返回（原图, 缩略图）路径。
func (p Paths) EnsureImageDirs(hash string) (origin, thumb string, err error) {
	origin, thumb = p.ImageOrigin(hash), p.ImageThumbnail(hash)
	for _, path := range []string{origin, thumb} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", "", err
		}
	}
	return origin, thumb, nil
}

// ImageName 是契约 4：items.content 里存的图片文件名。
func imageName(hash string) string { return hash + ".png" }

// shard 是分片目录名：哈希前 2 个字符（避免单个目录堆几十万文件）。
func shard(hash string) string {
	if len(hash) >= 2 {
		return hash[:2]
	}
	return "00"
}

// platformRoot 是纯函数版：不读环境，便于测试三分支。
func platformRoot(goos, appdata, xdg, home string) string {
	if home == "" {
		home = "."
	}
	switch {
	case goos == "darwin":
		return filepath.Join(home, "Library", "Application Support", AppDirName)
	case goos == "windows" && appdata != "":
		return filepath.Join(appdata, AppDirName)
	case xdg != "":
		return filepath.Join(xdg, AppDirName)
	default:
		return filepath.Join(home, ".local", "share", AppDirName)
	}
}
