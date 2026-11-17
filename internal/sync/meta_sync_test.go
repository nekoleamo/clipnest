package sync

import (
	"testing"

	"clipnest/internal/backup"
	"clipnest/internal/db"
	"clipnest/internal/model"
)

// TestMetaChangeReachesOtherDevice 先同步一轮，之后只改元数据（置顶/收藏/备注），
// 下一轮增量必须把这次改动带到另一台设备上。
//
// 回归用：曾经 ExportChanges 只看 updated_at，而契约 6 规定元数据改动不刷新
// updated_at，于是「改个备注」永远不出本机。
func TestMetaChangeReachesOtherDevice(t *testing.T) {
	remote := t.TempDir()
	a, b := openDB(t), openDB(t)
	pa, pb := tempPaths(t, "a"), tempPaths(t, "b")
	tA := NewLocalTransport(remote)

	res, err := db.Upsert(a, model.NewText("元数据要能同步"))
	if err != nil {
		t.Fatal(err)
	}
	// 第一轮：内容推上去，B 拉到
	if err := tA.Put("A.json", mustSeal(t, a)); err != nil {
		t.Fatal(err)
	}
	if _, err := syncRound(b, tA, "B", pb, backup.Plain, backup.Plain); err != nil {
		t.Fatal(err)
	}
	// A 侧记下「已导出到这里」，之后只改元数据
	since := model.Now()
	if err := setLastExport(a, since); err != nil {
		t.Fatal(err)
	}

	yes := true
	note := "这是后补的备注"
	if err := db.UpdateMeta(a, res.ID, db.MetaPatch{Pinned: &yes, Note: &note}); err != nil {
		t.Fatal(err)
	}
	// 契约 6：元数据改动不刷新 updated_at（列表顺序不变）
	item, ok, err := db.Get(a, res.ID)
	if err != nil || !ok {
		t.Fatalf("取条目失败：%v", err)
	}
	if item.UpdatedAt > since {
		t.Fatalf("元数据改动不该刷新 updated_at：%s > %s", item.UpdatedAt, since)
	}

	// 第二轮：改动应当被增量导出
	changes, err := ExportChanges(a, &since)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Item == nil || !changes[0].Item.Pinned {
		t.Fatalf("增量导出里应当有这次置顶：%+v", changes)
	}
	if err := tA.Put("A2.json", mustSeal(t, a)); err != nil {
		t.Fatal(err)
	}
	report, err := syncRound(b, tA, "B", pb, backup.Plain, backup.Plain)
	if err != nil {
		t.Fatal(err)
	}
	if report.Applied.Updated != 1 {
		t.Fatalf("B 应当更新 1 条，实际 %+v", report.Applied)
	}
	got, ok, err := db.Get(b, res.ID)
	if err != nil || !ok {
		t.Fatalf("B 上取条目失败：%v", err)
	}
	if !got.Pinned || got.Note != note {
		t.Fatalf("B 上的元数据没同步：pinned=%v note=%q", got.Pinned, got.Note)
	}
	_ = pa
}
