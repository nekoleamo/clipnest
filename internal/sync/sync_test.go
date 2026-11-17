package sync

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"

	"clipnest/internal/backup"
	"clipnest/internal/db"
	"clipnest/internal/model"
	"clipnest/internal/paths"
)

// sqlDB 是 *sql.DB 的别名。
type sqlDB = sql.DB

func openDB(t *testing.T) *sqlDB {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func tempPaths(t *testing.T, tag string) paths.Paths {
	t.Helper()
	p := paths.FromRoot(filepath.Join(t.TempDir(), tag))
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsAreOffAndEncrypted(t *testing.T) {
	c := DefaultConfig()
	if c.Enabled {
		t.Fatal("默认应关闭")
	}
	if !c.Encryption.Enabled {
		t.Fatal("默认应加密")
	}
	if c.Backend.Kind != BackendOff {
		t.Fatalf("默认后端：%s", c.Backend.Kind)
	}
	if c.IntervalSecs != 1800 {
		t.Fatalf("默认间隔：%d", c.IntervalSecs)
	}
}

func TestNormalizeTrimsAndSanitizes(t *testing.T) {
	c := DefaultConfig()
	c.DeviceName = " 我的 Mac / 二  "
	c.Backend = Backend{Kind: BackendWebDav, URL: " https://nas.local/dav/ ", RemoteDir: "clipnest"}
	c = c.Normalize()
	if c.Backend.URL != "https://nas.local/dav" {
		t.Fatalf("URL 没规范化：%q", c.Backend.URL)
	}
	if c.Backend.RemoteDir != "/clipnest" {
		t.Fatalf("远端目录：%q", c.Backend.RemoteDir)
	}
	for _, r := range c.DeviceName {
		if r == ' ' || r == '/' {
			t.Fatalf("设备名残留不安全字符：%q", c.DeviceName)
		}
	}
	if c.Version != ConfigVersion {
		t.Fatalf("版本：%d", c.Version)
	}
}

func TestReadyReportsWhatIsMissing(t *testing.T) {
	c := DefaultConfig()
	if err := c.Ready(); err == nil {
		t.Fatal("未启用应报错")
	}
	c.Enabled = true
	if err := c.Ready(); err == nil {
		t.Fatal("没选后端应报错")
	}
	c.Backend = Backend{Kind: BackendWebDav}
	if err := c.Ready(); err == nil {
		t.Fatal("缺地址应报错")
	}
	c.Backend.URL = "https://nas.local"
	if err := c.Ready(); err == nil {
		t.Fatal("缺用户名应报错")
	}
	c.Backend.Username = "u"
	if err := c.Ready(); err != nil {
		t.Fatalf("填全了应通过：%v", err)
	}
}

// noopThumb 之后的两台设备交互：A 的记录经远端被 B 拉到。
func TestTwoDevicesExchangeItems(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote")
	a, b := openDB(t), openDB(t)
	pa, pb := tempPaths(t, "a"), tempPaths(t, "b")

	res, err := db.Upsert(a, model.NewText("来自 A 的一条"))
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := db.UpdateMeta(a, res.ID, db.MetaPatch{Pinned: &yes}); err != nil {
		t.Fatal(err)
	}

	tA := NewLocalTransport(remote)
	if err := tA.Put("A.json", mustSeal(t, a)); err != nil {
		t.Fatal(err)
	}

	report, err := syncRound(b, tA, "B", pb, backup.Plain, backup.Plain)
	if err != nil {
		t.Fatal(err)
	}
	if report.Pulled != 1 || report.Applied.Inserted != 1 {
		t.Fatalf("拉取报告：%+v", report)
	}
	items, err := db.Query(b, "", db.Filter{}, db.DefaultPage)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Content != "来自 A 的一条" || !items[0].Pinned {
		t.Fatalf("B 上的条目：%+v", items)
	}
	_ = pa
}

// 幂等：同一份包重复合并，结果不变。
func TestMergeIsIdempotent(t *testing.T) {
	dbB := openDB(t)
	item := SyncItem{
		ContentHash: model.ContentHash(model.KindText, "重复合并"),
		Kind:        string(model.KindText),
		Content:     "重复合并",
		UseCount:    2,
		CreatedAt:   "2026-01-01T00:00:00.000Z",
		UpdatedAt:   "2026-01-01T00:00:00.000Z",
	}
	changes := []Change{{Op: "upsert", Item: &item}}

	first, err := ApplyChanges(dbB, changes)
	if err != nil {
		t.Fatal(err)
	}
	if first.Inserted != 1 {
		t.Fatalf("首次：%+v", first)
	}
	second, err := ApplyChanges(dbB, changes)
	if err != nil {
		t.Fatal(err)
	}
	if second.Inserted != 0 || second.Skipped != 1 {
		t.Fatalf("重复合并应全部跳过：%+v", second)
	}
	if n, _ := db.Count(dbB); n != 1 {
		t.Fatalf("条数：%d", n)
	}
}

// use_count 取两边最大值：多次拉取不会膨胀。
func TestUseCountTakesMaximum(t *testing.T) {
	dbB := openDB(t)
	local, _ := db.Upsert(dbB, model.NewText("使用次数"))
	if err := db.Touch(dbB, local.ID); err != nil { // 本地 2 次
		t.Fatal(err)
	}

	remote := SyncItem{
		ContentHash: model.ContentHash(model.KindText, "使用次数"),
		Kind:        string(model.KindText),
		Content:     "使用次数",
		UseCount:    5,
		CreatedAt:   "2026-01-01T00:00:00.000Z",
		UpdatedAt:   "2026-01-01T00:00:00.000Z",
	}
	report, err := ApplyChanges(dbB, []Change{{Op: "upsert", Item: &remote}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Updated != 1 {
		t.Fatalf("应更新 use_count：%+v", report)
	}
	item, _, _ := db.Get(dbB, local.ID)
	if item.UseCount != 5 {
		t.Fatalf("use_count 应为 5，得 %d", item.UseCount)
	}

	// 再来一次不会长到 10
	if _, err := ApplyChanges(dbB, []Change{{Op: "upsert", Item: &remote}}); err != nil {
		t.Fatal(err)
	}
	item, _, _ = db.Get(dbB, local.ID)
	if item.UseCount != 5 {
		t.Fatalf("重复合并后 use_count 应仍是 5，得 %d", item.UseCount)
	}
}

// 墓碑传播：B 上删了，A 拉到后也删；且不会被旧数据复活。
func TestTombstoneDeletesAndBlocksResurrection(t *testing.T) {
	dbA := openDB(t)
	res, _ := db.Upsert(dbA, model.NewText("将被删除"))
	item, _, _ := db.Get(dbA, res.ID)
	hash := item.ContentHash
	updatedAt := item.UpdatedAt

	// 别人的墓碑比本地条目新 → 删除
	report, err := ApplyChanges(dbA, []Change{{Op: "delete", ContentHash: hash, DeletedAt: "2999-01-01T00:00:00.000Z"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Deleted != 1 {
		t.Fatalf("应删掉：%+v", report)
	}
	if _, ok, _ := db.Get(dbA, res.ID); ok {
		t.Fatal("条目还在")
	}

	// 旧数据回来：被墓碑挡住
	stale := SyncItem{
		ContentHash: hash,
		Kind:        string(model.KindText),
		Content:     "将被删除",
		CreatedAt:   updatedAt,
		UpdatedAt:   updatedAt,
	}
	again, err := ApplyChanges(dbA, []Change{{Op: "upsert", Item: &stale}})
	if err != nil {
		t.Fatal(err)
	}
	if again.Inserted != 0 {
		t.Fatalf("墓碑应挡住复活：%+v", again)
	}
	if n, _ := db.Count(dbA); n != 0 {
		t.Fatalf("条数：%d", n)
	}
}

// 分组按名字对齐，不照搬 id。
func TestGroupsAlignByName(t *testing.T) {
	dbB := openDB(t)
	if _, err := dbB.Exec("INSERT INTO groups(name) VALUES ('工作')"); err != nil {
		t.Fatal(err)
	}
	name := "工作"
	item := SyncItem{
		ContentHash: model.ContentHash(model.KindText, "分组条目"),
		Kind:        string(model.KindText),
		Content:     "分组条目",
		Group:       &name,
		CreatedAt:   "2026-01-01T00:00:00.000Z",
		UpdatedAt:   "2026-01-01T00:00:00.000Z",
	}
	if _, err := ApplyChanges(dbB, []Change{{Op: "upsert", Item: &item}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := dbB.QueryRow("SELECT count(*) FROM groups").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("分组应复用同名的那个，实际 %d 个", count)
	}
}

func TestEncryptedRoundTripThroughTransport(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote")
	seal, open := backup.Sealer("口令123")

	pkg := Package{Version: FormatVersion, DeviceID: "A", ExportedAt: model.Now(), Changes: []Change{
		{Op: "delete", ContentHash: "abc", DeletedAt: "2026-01-01T00:00:00.000Z"},
	}}
	plain, err := MarshalPackage(pkg)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := seal(plain)
	if err != nil {
		t.Fatal(err)
	}

	tr := NewLocalTransport(remote)
	if err := tr.Put("A.json", sealed); err != nil {
		t.Fatal(err)
	}
	names, err := tr.List()
	if err != nil || len(names) != 1 || names[0] != "A.json" {
		t.Fatalf("列表：%v %v", names, err)
	}
	got, err := tr.Get("A.json")
	if err != nil {
		t.Fatal(err)
	}
	round, err := open(got)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalPackage(round)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Changes) != 1 || back.Changes[0].ContentHash != "abc" {
		t.Fatalf("载荷：%+v", back)
	}
}

func TestBlobPathShards(t *testing.T) {
	if got := BlobPath("abc123"); got != "blobs/ab/abc123.png" {
		t.Fatalf("blob 路径：%s", got)
	}
	if got := BlobPath("a"); got != "blobs/a/a.png" {
		t.Fatalf("短 hash：%s", got)
	}
}

// mustSeal 把 A 的增量包封好（不加密，测试只关心合并）。
func mustSeal(t *testing.T, database *sqlDB) []byte {
	t.Helper()
	changes, err := ExportChanges(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkg := Package{Version: FormatVersion, DeviceID: "A", ExportedAt: model.Now(), Changes: changes}
	data, err := MarshalPackage(pkg)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestRemoteNameDecodesHref PROPFIND 的 href 是 URL 编码的：带空格/中文的包名要能认出来。
func TestRemoteNameDecodesHref(t *testing.T) {
	cases := []struct{ href, want string }{
		{"/clipnest/", ""},
		{"/clipnest/A.json", "A.json"},
		{"/clipnest/my%20mac.json", "my mac.json"},
		{"/clipnest/%E5%8F%B0%E5%BC%8F%E6%9C%BA.json", "台式机.json"},
		{"https://nas.local/dav/clipnest/a%20b.json", "a b.json"},
	}
	for _, tc := range cases {
		if got := remoteName(tc.href); got != tc.want {
			t.Fatalf("remoteName(%q) = %q，期望 %q", tc.href, got, tc.want)
		}
	}
}

// TestReadLimitedRejectsOversize 超过上限的远端响应要报错，而不是把内存吃光。
func TestReadLimitedRejectsOversize(t *testing.T) {
	big := bytes.Repeat([]byte("x"), maxRemoteBytes+10)
	if _, err := readLimited(bytes.NewReader(big)); err == nil {
		t.Fatal("超过上限应当报错")
	}
	ok := bytes.Repeat([]byte("x"), 1024)
	if data, err := readLimited(bytes.NewReader(ok)); err != nil || len(data) != 1024 {
		t.Fatalf("正常大小应当读出来：%v len=%d", err, len(data))
	}
}
