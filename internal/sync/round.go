package sync

import (
	"database/sql"
	"errors"
	"os"
	"strings"

	"clipnest/internal/backup"
	"clipnest/internal/model"
	"clipnest/internal/paths"
)

// RunOnce 按配置跑一轮同步（建后端、取口令、选编解码）。
//
// UI 与定时任务都走这一条；错误都转成人能看懂的字符串。
func RunOnce(db *sql.DB, cfg Config, p paths.Paths) (SyncReport, error) {
	if err := cfg.Ready(); err != nil {
		return SyncReport{}, err
	}
	transport, ep, err := OpenTransport(cfg)
	if err != nil {
		return SyncReport{}, err
	}
	device := cfg.DeviceName

	if cfg.Encryption.Enabled {
		passphrase, err := secretGet(SecretService, device)
		if err != nil || passphrase == "" {
			return SyncReport{}, errors.New("钥匙串里没有同步口令（先在设置里填一个）")
		}
		seal, open := backup.Sealer(passphrase)
		return runRound(db, transport, ep, device, p, seal, open, cfg)
	}
	return runRound(db, transport, ep, device, p, backup.Plain, backup.Plain, cfg)
}

// runRound 跑一轮并把入口写进报告；失败时清掉端点缓存，下一轮重新探测。
func runRound(db *sql.DB, transport Transport, ep Endpoint, deviceID string, p paths.Paths,
	encode, decode Coder, cfg Config) (SyncReport, error) {
	report, err := syncRound(db, transport, deviceID, p, encode, decode)
	report.Endpoint = ep.String()
	if err != nil {
		ForgetEndpoint(cfg.Backend)
		return report, err
	}
	Logf("SYNC via %s", report.Endpoint)
	return report, nil
}

// syncRound 把本地增量推上去，再把别的设备的增量拉下来合并。
func syncRound(db *sql.DB, transport Transport, deviceID string, p paths.Paths, encode, decode Coder) (SyncReport, error) {
	var report SyncReport

	// 1) 推：只发上次导出之后的变更
	since, err := lastExport(db)
	if err != nil {
		return report, err
	}
	changes, err := ExportChanges(db, since)
	if err != nil {
		return report, err
	}
	exportedAt := model.Now()
	if len(changes) > 0 {
		pkg := Package{
			Version:    FormatVersion,
			DeviceID:   deviceID,
			ExportedAt: exportedAt,
			Changes:    changes,
		}
		plain, err := MarshalPackage(pkg)
		if err != nil {
			return report, err
		}
		sealed, err := encode(plain)
		if err != nil {
			return report, err
		}
		if err := transport.Put(deviceID+".json", sealed); err != nil {
			return report, err
		}
		report.Pushed = len(pkg.Changes)
		report.BlobsPushed = pushBlobs(transport, p, pkg.Changes)
	}
	// 没有变更也要记时间点，否则下次会把老变更再发一遍。
	if err := setLastExport(db, exportedAt); err != nil {
		return report, err
	}

	// 2) 拉：别的设备的包
	names, err := transport.List()
	if err != nil {
		return report, err
	}
	for _, name := range names {
		if name == deviceID+".json" {
			continue
		}
		sealed, err := transport.Get(name)
		if err != nil {
			Logf("SYNC skip %s: %v", name, err)
			continue
		}
		plain, err := decode(sealed)
		if err != nil {
			Logf("SYNC skip %s: 解密失败", name)
			continue
		}
		pkg, err := UnmarshalPackage(plain)
		if err != nil {
			Logf("SYNC skip %s: %v", name, err)
			continue
		}
		if pkg.Version != FormatVersion {
			Logf("SYNC skip %s: 版本 %d 不支持", name, pkg.Version)
			continue
		}
		report.Pulled += len(pkg.Changes)
		applied, err := ApplyChanges(db, pkg.Changes)
		if err != nil {
			return report, err
		}
		report.Applied.merge(applied)
		report.BlobsPulled += pullBlobs(transport, p, pkg.Changes)
	}
	return report, nil
}

// lastExport 读上次导出的时间点。
func lastExport(db *sql.DB) (*string, error) {
	var at string
	err := db.QueryRow("SELECT value FROM meta WHERE key = 'sync_last_export'").Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &at, nil
}

func setLastExport(db *sql.DB, at string) error {
	_, err := db.Exec(`INSERT INTO meta(key, value) VALUES ('sync_last_export', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, at)
	return err
}

// BlobPath 是图片在远端的位置：blobs/<hash 前2位>/<hash>.png（与本地分片一致）。
func BlobPath(hash string) string {
	shard := hash
	if len(shard) > 2 {
		shard = shard[:2]
	}
	return "blobs/" + shard + "/" + hash + ".png"
}

// pushBlobs 把包里的图片原图传到远端。
func pushBlobs(transport Transport, p paths.Paths, changes []Change) int {
	sent := 0
	for _, change := range changes {
		if change.Op != "upsert" || change.Item == nil || change.Item.Kind != string(model.KindImage) {
			continue
		}
		hash := strings.TrimSuffix(change.Item.Content, ".png")
		data, err := os.ReadFile(p.ImageOrigin(hash))
		if err != nil {
			Logf("SYNC blob missing local: %s", hash)
			continue
		}
		if err := transport.Put(BlobPath(hash), data); err != nil {
			Logf("SYNC blob push failed %s: %v", hash, err)
			continue
		}
		sent++
	}
	return sent
}

// pullBlobs 拉取包里本地还缺的图片原图（并顺手生成缩略图）。
func pullBlobs(transport Transport, p paths.Paths, changes []Change) int {
	got := 0
	for _, change := range changes {
		if change.Op != "upsert" || change.Item == nil || change.Item.Kind != string(model.KindImage) {
			continue
		}
		hash := strings.TrimSuffix(change.Item.Content, ".png")
		origin := p.ImageOrigin(hash)
		if _, err := os.Stat(origin); err == nil {
			continue
		}
		data, err := transport.Get(BlobPath(hash))
		if err != nil {
			Logf("SYNC blob not on remote: %s", hash)
			continue
		}
		originPath, thumbPath, err := p.EnsureImageDirs(hash)
		if err != nil {
			Logf("SYNC blob dir failed %s: %v", hash, err)
			continue
		}
		if err := os.WriteFile(originPath, data, 0o644); err != nil {
			Logf("SYNC blob write failed %s: %v", hash, err)
			continue
		}
		// 缩略图失败不影响同步（面板退化成显示尺寸）
		_ = writeThumbnail(data, thumbPath)
		got++
	}
	return got
}

// writeThumbnail 由 store 提供（这里用回调避免循环导入）。
var writeThumbnail = func(data []byte, to string) error {
	_ = data
	_ = to
	return nil
}

// SetThumbnailWriter 让宿主注入缩略图实现（store 包）。
func SetThumbnailWriter(fn func(data []byte, to string) error) {
	if fn != nil {
		writeThumbnail = fn
	}
}
