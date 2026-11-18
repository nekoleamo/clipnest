package sync

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"clipnest/internal/backup"
	"clipnest/internal/db"
	"clipnest/internal/model"
)

// countingTransport 数一下拉了几个包。
type countingTransport struct {
	*LocalTransport
	gets int
	puts int
}

func (c *countingTransport) Put(name string, data []byte) error {
	c.puts++
	return c.LocalTransport.Put(name, data)
}

func (c *countingTransport) Get(name string) ([]byte, error) {
	c.gets++
	return c.LocalTransport.Get(name)
}

// TestPackageNameRoundTrip 包名是 <设备名>-<定长时刻>.json，能从末尾切回来；
// 设备名里带 '-' 也不能切错。
func TestPackageNameRoundTrip(t *testing.T) {
	at := "2026-11-19T01:23:45.678Z"
	for _, device := range []string{"mac-a", "my-mac-book-air-local", "台式机"} {
		name := packageName(device, at)
		gotDevice, gotStamp, ok := parsePackageName(name)
		if !ok {
			t.Fatalf("%q 应当认得出来", name)
		}
		if gotDevice != device {
			t.Fatalf("%q 拆出来的设备名是 %q", name, gotDevice)
		}
		if gotStamp != "20261119T012345.678" {
			t.Fatalf("时刻切错了：%q", gotStamp)
		}
	}
	// 旧格式（没有时刻）认不出来，但也不该当成错误
	for _, legacy := range []string{"mac-a.json", "A.json"} {
		if _, _, ok := parsePackageName(legacy); ok {
			t.Fatalf("%q 是旧格式，不该被当成带时刻的包", legacy)
		}
	}
}

// TestSyncKeepsDeltasWhilePeerOffline 对端离线期间推的每一包都要留着，
// 回来后能一次补全（以前每设备只有一个会被覆盖的文件，中间几轮会丢）。
func TestSyncKeepsDeltasWhilePeerOffline(t *testing.T) {
	remote := t.TempDir()
	a, b := openDB(t), openDB(t)
	pa, pb := tempPaths(t, "a"), tempPaths(t, "b")
	tA := NewLocalTransport(remote)

	push := func(text string) {
		t.Helper()
		if _, err := db.Upsert(a, model.NewText(text)); err != nil {
			t.Fatal(err)
		}
		if _, err := syncRound(a, tA, "mac-a", pa, backup.Plain, backup.Plain); err != nil {
			t.Fatal(err)
		}
	}
	push("离线期间的第一条")
	push("离线期间的第二条")

	files, err := tA.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("两轮增量应当是两包，实际 %d：%v", len(files), files)
	}

	// B 上线：一次把两包都补上
	report, err := syncRound(b, tA, "mac-b", pb, backup.Plain, backup.Plain)
	if err != nil {
		t.Fatal(err)
	}
	if report.Applied.Inserted != 2 {
		t.Fatalf("B 应当补到 2 条，实际 %+v", report.Applied)
	}
	items, err := db.Query(b, "", db.Filter{}, db.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	_ = items
	if n := countItems(t, b); n != 2 {
		t.Fatalf("B 上应当有 2 条，实际 %d", n)
	}
}

// TestSyncSkipsConsumedPackages 吃过的包不要每轮重复下载（按包名里的时刻记水位）。
func TestSyncSkipsConsumedPackages(t *testing.T) {
	remote := t.TempDir()
	a, b := openDB(t), openDB(t)
	pa, pb := tempPaths(t, "a"), tempPaths(t, "b")
	tA := NewLocalTransport(remote)
	tB := &countingTransport{LocalTransport: tA}

	if _, err := db.Upsert(a, model.NewText("一个包")); err != nil {
		t.Fatal(err)
	}
	if _, err := syncRound(a, tA, "mac-a", pa, backup.Plain, backup.Plain); err != nil {
		t.Fatal(err)
	}
	if _, err := syncRound(b, tB, "mac-b", pb, backup.Plain, backup.Plain); err != nil {
		t.Fatal(err)
	}
	if tB.gets == 0 {
		t.Fatal("第一轮应当下载对方的包")
	}
	tB.gets = 0
	if _, err := syncRound(b, tB, "mac-b", pb, backup.Plain, backup.Plain); err != nil {
		t.Fatal(err)
	}
	if tB.gets != 0 {
		t.Fatalf("已经合并过的包不该再下载，实际下载了 %d 个", tB.gets)
	}
}

// TestSyncCleansOldOwnPackages 自己的包超过保留期要删掉；别人的老包与自己新包不能动。
func TestSyncCleansOldOwnPackages(t *testing.T) {
	remote := t.TempDir()
	a := openDB(t)
	pa := tempPaths(t, "a")
	tA := NewLocalTransport(remote)

	oldAt := time.Now().UTC().AddDate(0, 0, -PackageKeepDays-1).Format(model.TimeLayout)
	freshAt := time.Now().UTC().Format(model.TimeLayout)
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(remote, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldOwn := packageName("mac-a", oldAt)
	oldPeer := packageName("mac-b", oldAt)
	freshOwn := packageName("mac-a", freshAt)
	write(oldOwn)
	write(oldPeer)
	write(freshOwn)

	if _, err := syncRound(a, tA, "mac-a", pa, backup.Plain, backup.Plain); err != nil {
		t.Fatal(err)
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(remote, name))
		return err == nil
	}
	if exists(oldOwn) {
		t.Fatalf("自己超过 %d 天的包应当被删掉：%s", PackageKeepDays, oldOwn)
	}
	if !exists(oldPeer) {
		t.Fatal("别人的包不该被本机删掉")
	}
	if !exists(freshOwn) {
		t.Fatal("自己没到期的包不该删")
	}
}

// countItems 数本地库里的条目数。
func countItems(t *testing.T, database *sqlDB) int {
	t.Helper()
	var n int
	if err := database.QueryRow("SELECT count(*) FROM items").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestBlobPushIsIdempotent 图片原图只推一次：条目再改（备注/置顶）也不整张重传，
// 否则「重新推送全部历史」= 把几百兆图片重上一遍。
func TestBlobPushIsIdempotent(t *testing.T) {
	remote := t.TempDir()
	a := openDB(t)
	pa := tempPaths(t, "a")
	tA := &countingTransport{LocalTransport: NewLocalTransport(remote)}

	hash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	origin, _, err := pa.EnsureImageDirs(hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(origin, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := db.Upsert(a, model.NewItem{Kind: model.KindImage, Content: hash + ".png", Width: 8, Height: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncRound(a, tA, "mac-a", pa, backup.Plain, backup.Plain); err != nil {
		t.Fatal(err)
	}
	if tA.puts == 0 {
		t.Fatal("第一次应当把原图传上去")
	}

	// 改个备注让这条图片重新进增量，再推一轮
	note := "改个备注"
	if err := db.UpdateMeta(a, res.ID, db.MetaPatch{Note: &note}); err != nil {
		t.Fatal(err)
	}
	tA.puts = 0
	report, err := syncRound(a, tA, "mac-a", pa, backup.Plain, backup.Plain)
	if err != nil {
		t.Fatal(err)
	}
	if report.Pushed != 1 {
		t.Fatalf("条目应当重新进增量，实际推了 %d", report.Pushed)
	}
	if report.BlobsPushed != 0 || tA.puts != 1 {
		t.Fatalf("原图已经在远端，不该重传：BlobsPushed=%d puts=%d", report.BlobsPushed, tA.puts)
	}
}
