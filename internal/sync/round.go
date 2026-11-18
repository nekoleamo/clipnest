package sync

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

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
			return SyncReport{}, fmt.Errorf("%s里没有同步口令（先在设置里填一个）", SecretStoreName())
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
	// 这里不再每轮写日志：常驻进程 5 秒一轮，安静的时候不该刷屏。
	// 调用方（app.runSync）只在「有变化」时才记一行，含入口在内。
	return report, nil
}

// PackageKeepDays 是本机留多少天的增量包；更早的由本机自己删掉（别人的包一律不碰）。
//
// 留几天的意义：对端离线期间它该拿的增量都还在。对端离线超过这么多天，那几包就没了
// —— 剪贴板历史按「一次性」对待，这个取舍写在这里，别当成可靠消息队列。
const PackageKeepDays = 7

// stampLayout 是包名里的时刻：定长 19 位（含毫秒），方便从文件名末尾切出来。
const stampLayout = "20060102T150405.000"

// stampOf 把 model.Now() 的时刻变成包名里的定长时间戳（解析不了就用当前时刻）。
func stampOf(at string) string {
	t, err := time.Parse(model.TimeLayout, at)
	if err != nil {
		return time.Now().UTC().Format(stampLayout)
	}
	return t.UTC().Format(stampLayout)
}

// packageName 是远端包名：<设备名>-<时刻>.json。
//
// 设备名在前、时刻在末尾定长：本机能按前缀认出自己的包来清理，也能从末尾切出时刻。
func packageName(device, at string) string { return device + "-" + stampOf(at) + ".json" }

// parsePackageName 拆包名；旧格式（只有 <设备名>.json，没有时刻）返回 ok=false，
// 那种包照样会被拉下来合并，只是没法按时刻跳过/清理。
func parsePackageName(name string) (device, stamp string, ok bool) {
	base := strings.TrimSuffix(name, ".json")
	n := len(stampLayout)
	if len(base) < n+2 || base[len(base)-n-1] != '-' {
		return "", "", false
	}
	stamp = base[len(base)-n:]
	if _, err := time.Parse(stampLayout, stamp); err != nil {
		return "", "", false
	}
	return base[:len(base)-n-1], stamp, true
}

// seenStamp 是本机已经吃过的、某设备最新的包时刻。
func seenStamp(db *sql.DB, device string) string {
	var at string
	err := db.QueryRow("SELECT value FROM meta WHERE key = ?", "sync_seen_"+device).Scan(&at)
	if err != nil {
		return ""
	}
	return at
}

// setSeenStamp 记下「这台设备的包吃到这一刻了」。
func setSeenStamp(db *sql.DB, device, stamp string) error {
	_, err := db.Exec(`INSERT INTO meta(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, "sync_seen_"+device, stamp)
	return err
}

// freePackageName 返回一个还没被占用的包名：同一毫秒里连推两轮时把时刻往后挪一格，
// 否则后一轮会把前一轮覆盖掉（那就又变成「只留最近一次增量」了）。
func freePackageName(device, at string, existing []string) string {
	name := packageName(device, at)
	taken := map[string]bool{}
	for _, n := range existing {
		taken[n] = true
	}
	ts, err := time.Parse(stampLayout, stampOf(at))
	if err != nil {
		return name
	}
	for taken[name] {
		ts = ts.Add(time.Millisecond)
		name = packageName(device, ts.UTC().Format(model.TimeLayout))
	}
	return name
}

// syncRound 把本地增量推上去，再把别的设备的增量拉下来合并。
func syncRound(db *sql.DB, transport Transport, deviceID string, p paths.Paths, encode, decode Coder) (SyncReport, error) {
	var report SyncReport

	// 先看一眼远端有哪些包：推的时候要避开已占用的名字，拉的时候用同一份清单
	names, err := transport.List()
	if err != nil {
		return report, err
	}

	// 1) 推：只发上次导出之后的变更，一包一个文件（<设备名>-<时刻>.json），互不覆盖
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
		if err := transport.Put(freePackageName(deviceID, exportedAt, names), sealed); err != nil {
			return report, err
		}
		report.Pushed = len(pkg.Changes)
		report.BlobsPushed = pushBlobs(transport, p, pkg.Changes)
	}
	// 没有变更也要记时间点，否则下次会把老变更再发一遍。
	if err := setLastExport(db, exportedAt); err != nil {
		return report, err
	}

	// 2) 拉：别人的包；自己的、以及已经吃过的按包名跳过
	for _, name := range names {
		owner, stamp, stamped := parsePackageName(name)
		if stamped {
			if owner == deviceID {
				continue // 自己的包
			}
			if stamp <= seenStamp(db, owner) {
				continue // 这一包（以及更早的）已经合并过了
			}
		} else if name == deviceID+".json" {
			continue // 旧格式的自打包
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
		// 吃起来哪一包就记到哪：下一轮不再重复下载（合并是幂等的，重复也没事，只是费流量）
		at := stampOf(pkg.ExportedAt)
		if stamped {
			at = stamp
		}
		if err := setSeenStamp(db, pkg.DeviceID, at); err != nil {
			return report, err
		}
	}

	// 3) 清理：自己的包留 PackageKeepDays 天，别人的一律不碰。
	//    删的原因是文件名不再覆盖，长期用下来会越攒越多。
	cleanupOwnPackages(transport, deviceID, names)
	return report, nil
}

// cleanupOwnPackages 删掉本机超过保留期的增量包。
//
// 只删自己的：别人的包什么时候能删，由那台机器自己判断（它才知道对端拉没拉过）。
func cleanupOwnPackages(transport Transport, device string, names []string) {
	cutoff := time.Now().UTC().AddDate(0, 0, -PackageKeepDays).Format(stampLayout)
	for _, name := range names {
		owner, stamp, ok := parsePackageName(name)
		if !ok || owner != device || stamp >= cutoff {
			continue
		}
		if err := transport.Remove(name); err != nil {
			Logf("SYNC 清理旧包 %s 失败：%v", name, err)
		}
	}
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
