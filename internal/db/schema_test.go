package db

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int64
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = ?", name).Scan(&n); err != nil {
		t.Fatalf("查表 %s: %v", name, err)
	}
	return n > 0
}

// T2 验收：建库后所有表都在，版本号到位。
func TestFreshDBHasFullSchema(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	v, err := Version(db)
	if err != nil {
		t.Fatal(err)
	}
	if v != SchemaVersion {
		t.Fatalf("版本 %d，期望 %d", v, SchemaVersion)
	}
	for _, name := range []string{"items", "groups", "meta", "items_fts", "tombstones"} {
		if !tableExists(t, db, name) {
			t.Fatalf("缺表 %s", name)
		}
	}
	if id, err := InstallID(db); err != nil || id == "" {
		t.Fatalf("install_id=%q err=%v", id, err)
	}
	// 再迁一次是空操作
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if v, _ := Version(db); v != SchemaVersion {
		t.Fatalf("幂等迁移后版本 %d", v)
	}
}

// T2 验收：版本从 0 推进，已有数据不丢。
func TestMigrateAdvancesVersionAndKeepsData(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := tune(db); err != nil {
		t.Fatal(err)
	}
	if v, _ := Version(db); v != 0 {
		t.Fatalf("新库版本应为 0，得 %d", v)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if v, _ := Version(db); v != SchemaVersion {
		t.Fatalf("版本 %d", v)
	}

	if _, err := db.Exec(`INSERT INTO items(content_hash, kind, content, created_at, updated_at)
		VALUES ('h1','text','老数据','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	// 模拟旧库：版本号被退回 0 后再迁移，数据仍在
	if _, err := db.Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.QueryRow("SELECT count(*) FROM items").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("数据丢了：%d 行", n)
	}
}

// 未来版本（用户装过更新的 clipnest）必须报错，不许降级写。
func TestRefusesToDowngrade(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA user_version = " + fmt.Sprint(SchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	err = Migrate(db)
	if err == nil {
		t.Fatal("应当报错")
	}
	if !errors.Is(err, ErrTooNew) {
		t.Fatalf("错误类型不对：%v", err)
	}
}

// T2 验收：触发器让 FTS 跟着 items 增/改/删同步。
func TestFTSTracksInsertUpdateDelete(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	hit := func(q string) int64 {
		var n int64
		if err := db.QueryRow("SELECT count(*) FROM items_fts WHERE items_fts MATCH ?", q).Scan(&n); err != nil {
			t.Fatalf("查 %q: %v", q, err)
		}
		return n
	}
	insert := func(content string) int64 {
		res, err := db.Exec(`INSERT INTO items(content_hash, kind, content, created_at, updated_at)
			VALUES (?, 'text', ?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, content, content)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	// 增
	id := insert("hello clipnest 银河系")
	if got := hit("clipnest"); got != 1 {
		t.Fatalf("英文命中 %d", got)
	}
	if got := hit("银河系"); got != 1 {
		t.Fatalf("trigram 应支持中文子串（3 字符起），得 %d", got)
	}
	if got := hit("银河"); got != 0 {
		t.Fatalf("2 字符短词 trigram 命中不了，得 %d", got)
	}

	// 改
	if _, err := db.Exec("UPDATE items SET content = 'goodbye world' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if got := hit("clipnest"); got != 0 {
		t.Fatalf("旧内容必须从索引里消失，得 %d", got)
	}
	if got := hit("goodbye"); got != 1 {
		t.Fatalf("新内容未入索引 %d", got)
	}

	// 删
	if _, err := db.Exec("DELETE FROM items WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if got := hit("goodbye"); got != 0 {
		t.Fatalf("删除后仍在索引 %d", got)
	}

	// 备注也进索引
	id = insert("正文")
	if _, err := db.Exec("UPDATE items SET note = 'tag-银河系' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if got := hit("银河系"); got != 1 {
		t.Fatalf("备注未进索引 %d", got)
	}
}

// 库若被外部工具改坏索引，rebuild 能救回来。
func TestRebuildRepairsFTS(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO items(content_hash, kind, content, created_at, updated_at)
		VALUES ('h','text','孤岛数据之岛','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO items_fts(items_fts) VALUES ('delete-all')"); err != nil {
		t.Fatal(err)
	}
	count := func() int64 {
		var n int64
		if err := db.QueryRow("SELECT count(*) FROM items_fts WHERE items_fts MATCH '孤岛数'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("清空后应为 0，得 %d", n)
	}
	if err := RebuildFTS(db); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("重建后应为 1，得 %d", n)
	}
}
