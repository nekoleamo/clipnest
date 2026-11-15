package backup

import (
	"bytes"
	"database/sql"
	"errors"
	"testing"

	"clipnest/internal/db"
	"clipnest/internal/model"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestContainerStartsWithMagic(t *testing.T) {
	blob, err := Seal([]byte("hello"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(blob, Magic) {
		t.Fatalf("容器头：%q", blob[:5])
	}
	if len(blob) <= len(Magic)+SaltLen+NonceLen {
		t.Fatalf("容器太短：%d", len(blob))
	}
}

func TestSealOpenRoundTripAndWrongPassphrase(t *testing.T) {
	plain := []byte(`{"a":1}`)
	blob, err := Seal(plain, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(blob, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("往返失败：%q", got)
	}
	if _, err := Open(blob, "wrong"); !errors.Is(err, ErrCrypto) {
		t.Fatalf("错口令应返回 ErrCrypto：%v", err)
	}
}

func TestTamperedHeaderIsRejected(t *testing.T) {
	blob, err := Seal([]byte("x"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), blob...)
	bad[0] = 'X'
	if _, err := Open(bad, "pw"); !errors.Is(err, ErrFormat) {
		t.Fatalf("改 magic 应报格式错误：%v", err)
	}
	// 改密文：AEAD 认证失败
	bad = append([]byte(nil), blob...)
	bad[len(bad)-1] ^= 0xFF
	if _, err := Open(bad, "pw"); !errors.Is(err, ErrCrypto) {
		t.Fatalf("改密文应报 ErrCrypto：%v", err)
	}
}

func TestExportImportRoundTripsItemsAndGroups(t *testing.T) {
	src := openDB(t)
	group, err := src.Exec("INSERT INTO groups(name, sort) VALUES ('工作', 3)")
	if err != nil {
		t.Fatal(err)
	}
	gid, _ := group.LastInsertId()
	res, err := db.Upsert(src, model.NewText("要备份的文本"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMeta(src, res.ID, db.MetaPatch{GroupID: &gid}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert(src, model.NewImage("abc.png", 80, 60)); err != nil {
		t.Fatal(err)
	}

	blob, err := ExportBytes(src, "pw")
	if err != nil {
		t.Fatal(err)
	}

	dst := openDB(t)
	report, err := ImportBytes(dst, blob, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if report.Inserted != 2 || report.Skipped != 0 || report.GroupsInserted != 1 {
		t.Fatalf("导入报告：%+v", report)
	}
	if n, _ := db.Count(dst); n != 2 {
		t.Fatalf("导入后条数：%d", n)
	}

	// 再导入一次：全部跳过（幂等）
	again, err := ImportBytes(dst, blob, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if again.Inserted != 0 || again.Skipped != 2 {
		t.Fatalf("重复导入：%+v", again)
	}
}

func TestImportKeepsLocalStateForExistingHash(t *testing.T) {
	src := openDB(t)
	res, _ := db.Upsert(src, model.NewText("同一个内容"))
	yes := true
	_ = db.UpdateMeta(src, res.ID, db.MetaPatch{Pinned: &yes})
	blob, err := ExportBytes(src, "pw")
	if err != nil {
		t.Fatal(err)
	}

	dst := openDB(t)
	local, _ := db.Upsert(dst, model.NewText("同一个内容"))
	report, err := ImportBytes(dst, blob, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 {
		t.Fatalf("应跳过已有条目：%+v", report)
	}
	item, _, _ := db.Get(dst, local.ID)
	if item.Pinned {
		t.Fatal("导入不该覆盖本地元数据")
	}
}

func TestDefaultNameHasTimestampAndSuffix(t *testing.T) {
	name := DefaultName()
	if len(name) != len("clipnest-20060102-150405.cnbak") {
		t.Fatalf("文件名：%s", name)
	}
	if name[len(name)-6:] != ".cnbak" {
		t.Fatalf("后缀：%s", name)
	}
}
