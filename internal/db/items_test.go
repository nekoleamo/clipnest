package db

import (
	"database/sql"
	"testing"
	"time"

	"clipnest/internal/model"
)

// sqlDB 是 *sql.DB 的别名，测试里用短名字。
type sqlDB = sql.DB

func openTest(t *testing.T) *sqlDB {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestSameContentThreeTimesIsOneRow(t *testing.T) {
	database := openTest(t)
	var results []UpsertResult
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(2 * time.Millisecond) // 毫秒时间戳，同毫秒分不出先后
		}
		r, err := Upsert(database, model.NewText("复制的同一句话"))
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, r)
	}
	if !results[0].Inserted {
		t.Fatal("首次应是 INSERTED")
	}
	for _, r := range results[1:] {
		if r.Inserted || r.ID != results[0].ID {
			t.Fatalf("去重结果不对：%+v", r)
		}
	}
	if n, _ := Count(database); n != 1 {
		t.Fatalf("行数 %d", n)
	}

	item, ok, err := Get(database, results[0].ID)
	if err != nil || !ok {
		t.Fatalf("取行失败：%v", err)
	}
	if item.UseCount != 3 {
		t.Fatalf("use_count=%d", item.UseCount)
	}
	if !(item.UpdatedAt > item.CreatedAt) {
		t.Fatalf("去重要刷新 updated_at：%s vs %s", item.UpdatedAt, item.CreatedAt)
	}
}

func TestSameContentDifferentKindIsTwoRows(t *testing.T) {
	database := openTest(t)
	a, err := Upsert(database, model.NewText("<b>x</b>"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Upsert(database, model.NewHTML("<b>x</b>"))
	if err != nil {
		t.Fatal(err)
	}
	if !a.Inserted || !b.Inserted || a.ID == b.ID {
		t.Fatalf("不同类型应各自成行：%+v %+v", a, b)
	}
	if n, _ := Count(database); n != 2 {
		t.Fatalf("行数 %d", n)
	}
}

// 契约 5 + 契约 6 + 去重后用户改过的元数据不被覆盖。
func TestMetaUpdatesKeepUpdatedAtAndSurviveDedup(t *testing.T) {
	database := openTest(t)
	res, err := Upsert(database, model.NewText("要被复制的文本"))
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := Get(database, res.ID)

	yes := true
	note := "重要"
	if err := UpdateMeta(database, res.ID, MetaPatch{Pinned: &yes, Favorite: &yes, Note: &note}); err != nil {
		t.Fatal(err)
	}

	mid, _, _ := Get(database, res.ID)
	if !mid.Pinned || !mid.Favorite || mid.Note != "重要" {
		t.Fatalf("元数据没写进去：%+v", mid)
	}
	if mid.UpdatedAt != before.UpdatedAt {
		t.Fatalf("契约 6：元数据不刷新 updated_at（%s != %s）", mid.UpdatedAt, before.UpdatedAt)
	}

	// 再次入库：use_count 增加，但收藏/备注保持
	r, err := Upsert(database, model.NewText("要被复制的文本"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Inserted {
		t.Fatal("应命中去重")
	}
	after, _, _ := Get(database, res.ID)
	if after.UseCount != 2 || !after.Pinned || !after.Favorite || after.Note != "重要" {
		t.Fatalf("去重覆盖了用户元数据：%+v", after)
	}
}

func TestTouchCountsOneUseAndRefreshesTime(t *testing.T) {
	database := openTest(t)
	res, _ := Upsert(database, model.NewText("粘贴即使用"))
	before, _, _ := Get(database, res.ID)
	if err := Touch(database, res.ID); err != nil {
		t.Fatal(err)
	}
	after, _, _ := Get(database, res.ID)
	if after.UseCount != before.UseCount+1 {
		t.Fatalf("use_count=%d", after.UseCount)
	}
	if after.CreatedAt != before.CreatedAt {
		t.Fatal("created_at 不该变")
	}
}

func TestNoteAndGroupCanBeCleared(t *testing.T) {
	database := openTest(t)
	g, err := database.Exec("INSERT INTO groups(name) VALUES ('工作')")
	if err != nil {
		t.Fatal(err)
	}
	gid, _ := g.LastInsertId()
	res, _ := Upsert(database, model.NewText("x"))

	note := "n"
	if err := UpdateMeta(database, res.ID, MetaPatch{Note: &note, GroupID: &gid}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMeta(database, res.ID, MetaPatch{ClearNote: true, ClearGroup: true}); err != nil {
		t.Fatal(err)
	}
	item, _, _ := Get(database, res.ID)
	if item.Note != "" || item.GroupID != nil {
		t.Fatalf("清空失败：%+v", item)
	}
	// 空补丁是空操作，不报错
	if err := UpdateMeta(database, res.ID, MetaPatch{}); err != nil {
		t.Fatal(err)
	}
}

func TestImageAndFilesRoundTrip(t *testing.T) {
	database := openTest(t)
	img, err := Upsert(database, model.NewImage("abc.png", 1920, 1080))
	if err != nil {
		t.Fatal(err)
	}
	item, _, _ := Get(database, img.ID)
	if item.Kind != model.KindImage || item.Content != "abc.png" || item.Width != 1920 || item.Height != 1080 {
		t.Fatalf("图片条目：%+v", item)
	}

	files, err := Upsert(database, model.NewFiles([]string{"/tmp/a.png", "/tmp/b"}, "fd").WithSensitive(true))
	if err != nil {
		t.Fatal(err)
	}
	item, _, _ = Get(database, files.ID)
	if item.Kind != model.KindFiles || item.Content != "/tmp/a.png\n/tmp/b" ||
		item.FileTypes != "fd" || !item.Sensitive {
		t.Fatalf("文件条目：%+v", item)
	}
}

func TestDeleteRemovesRowAndFTS(t *testing.T) {
	database := openTest(t)
	res, _ := Upsert(database, model.NewText("待删除条目"))
	ok, err := Delete(database, res.ID)
	if err != nil || !ok {
		t.Fatalf("删除失败：%v", err)
	}
	if _, ok, _ := Get(database, res.ID); ok {
		t.Fatal("行还在")
	}
	if ok, _ := Delete(database, res.ID); ok {
		t.Fatal("重复删应返回 false")
	}
	var n int64
	if err := database.QueryRow("SELECT count(*) FROM items_fts WHERE items_fts MATCH '待删除'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("FTS 条目没删掉：%d", n)
	}
}
