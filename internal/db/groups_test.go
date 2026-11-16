package db

import "testing"

// TestEnsureGroupIsIdempotent 同名分组只会建一次。
func TestEnsureGroupIsIdempotent(t *testing.T) {
	conn := openTest(t)

	first, err := EnsureGroup(conn, "工作")
	if err != nil {
		t.Fatalf("建组失败：%v", err)
	}
	again, err := EnsureGroup(conn, " 工作 ")
	if err != nil {
		t.Fatalf("再取一次失败：%v", err)
	}
	if first != again {
		t.Fatalf("同名分组应当复用：%d vs %d", first, again)
	}
	if _, err := EnsureGroup(conn, "   "); err == nil {
		t.Fatal("空名字应当报错")
	}
}

// TestGroupsListRenameDelete 列表 / 改名 / 删除，删除后组内条目回落「未分组」。
func TestGroupsListRenameDelete(t *testing.T) {
	conn := openTest(t)
	workID, _ := EnsureGroup(conn, "工作")
	personalID, _ := EnsureGroup(conn, "私人")

	groups, err := ListGroups(conn)
	if err != nil || len(groups) != 2 {
		t.Fatalf("应有两个分组：%v %v", groups, err)
	}

	if err := RenameGroup(conn, workID, "公司"); err != nil {
		t.Fatalf("改名失败：%v", err)
	}
	groups, _ = ListGroups(conn)
	if groups[0].Name != "公司" {
		t.Fatalf("改名没生效：%+v", groups)
	}
	if err := RenameGroup(conn, workID, "  "); err == nil {
		t.Fatal("改成空名字应当报错")
	}

	// 放一条进组，再删组，应当变成未分组
	res, err := conn.Exec(`INSERT INTO items(content_hash, kind, content, created_at, updated_at, group_id)
		VALUES ('h1', 'text', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', ?)`, workID)
	if err != nil {
		t.Fatalf("插入失败：%v", err)
	}
	id, _ := res.LastInsertId()

	counts, err := GroupCounts(conn)
	if err != nil || counts[workID] != 1 {
		t.Fatalf("分组计数不对：%v %v", counts, err)
	}

	if err := DeleteGroup(conn, workID); err != nil {
		t.Fatalf("删组失败：%v", err)
	}
	var groupID any
	if err := conn.QueryRow("SELECT group_id FROM items WHERE id = ?", id).Scan(&groupID); err != nil {
		t.Fatalf("查条目失败：%v", err)
	}
	if groupID != nil {
		t.Fatalf("删组后条目应当变成未分组，实际 group_id=%v", groupID)
	}
	groups, _ = ListGroups(conn)
	if len(groups) != 1 || groups[0].ID != personalID {
		t.Fatalf("删组后应只剩一个：%+v", groups)
	}
}
