package db

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"clipnest/internal/model"
)

func seed(t *testing.T, database *sqlDB, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		var item model.NewItem
		switch i % 4 {
		case 0:
			item = model.NewText(fmt.Sprintf("中文条目 第%d号 银河系", i))
		case 1:
			item = model.NewText(fmt.Sprintf("hello world number %d", i))
		case 2:
			item = model.NewFiles([]string{fmt.Sprintf("/tmp/file%d.png", i)}, "f")
		default:
			item = model.NewHTML(fmt.Sprintf("<b>html %d</b>", i))
		}
		if _, err := Upsert(database, item); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestEmptyQueryListsNewestFirst(t *testing.T) {
	database := openTest(t)
	seed(t, database, 10)
	got, err := Query(database, "", Filter{}, DefaultPage)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("条数 %d", len(got))
	}
	if got[0].UpdatedAt < got[1].UpdatedAt {
		t.Fatal("应按 updated_at 倒序")
	}
}

func TestPinnedAlwaysFirst(t *testing.T) {
	database := openTest(t)
	seed(t, database, 6)
	all, err := Query(database, "", Filter{}, DefaultPage)
	if err != nil {
		t.Fatal(err)
	}
	oldest := all[len(all)-1]
	yes := true
	if err := UpdateMeta(database, oldest.ID, MetaPatch{Pinned: &yes}); err != nil {
		t.Fatal(err)
	}
	got, err := Query(database, "", Filter{}, DefaultPage)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != oldest.ID {
		t.Fatalf("契约 8：置顶要排最前，得 %d", got[0].ID)
	}
	if got[0].UpdatedAt != oldest.UpdatedAt {
		t.Fatal("契约 6：置顶不该刷新 updated_at")
	}
}

func TestSearchLongWordsHitsCJKEnglishAndSubstring(t *testing.T) {
	database := openTest(t)
	seed(t, database, 40)

	zh, _ := Query(database, "银河系", Filter{}, DefaultPage)
	if len(zh) != 10 {
		t.Fatalf("中文命中 %d", len(zh))
	}
	en, _ := Query(database, "hello", Filter{}, DefaultPage)
	if len(en) != 10 {
		t.Fatalf("英文命中 %d", len(en))
	}
	sub, _ := Query(database, "orld num", Filter{}, DefaultPage)
	if len(sub) != 10 {
		t.Fatalf("子串命中 %d", len(sub))
	}
}

func TestSearchShortWordsFallsBackToLike(t *testing.T) {
	database := openTest(t)
	seed(t, database, 12)
	got, err := Query(database, "银河", Filter{}, DefaultPage)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("2 字符应降级 LIKE，得 %d 条", len(got))
	}
	got, err = Query(database, "号", Filter{}, DefaultPage)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("单字应降级 LIKE，得 %d 条", len(got))
	}
	if n, err := CountQuery(database, "银河", Filter{}); err != nil || n != 3 {
		t.Fatalf("count=%d err=%v", n, err)
	}
}

func TestSearchSpecialCharsDoNotError(t *testing.T) {
	database := openTest(t)
	if _, err := Upsert(database, model.NewText("C:\\path\\to \"quoted\" file 100% _done_")); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`"quoted"`, "100%", "_done_", `C:\path`, "-not-", "a AND b"} {
		got, err := Query(database, q, Filter{}, DefaultPage)
		if err != nil {
			t.Fatalf("%q 查询报错：%v", q, err)
		}
		if len(got) > 1 {
			t.Fatalf("%q 命中过多：%d", q, len(got))
		}
	}
	// 字面匹配（转义生效，不是通配）
	for _, q := range []string{"100%", "_done_"} {
		got, _ := Query(database, q, Filter{}, DefaultPage)
		if len(got) != 1 {
			t.Fatalf("%q 应字面命中 1 条，得 %d", q, len(got))
		}
	}
}

func TestFiltersByKindGroupFavoriteAndSensitive(t *testing.T) {
	database := openTest(t)
	seed(t, database, 8)
	got, err := Query(database, "", Filter{Kinds: []model.ItemKind{model.KindFiles}}, DefaultPage)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("文件条目 %d", len(got))
	}
	for _, item := range got {
		if item.Kind != model.KindFiles {
			t.Fatalf("类型过滤失效：%s", item.Kind)
		}
	}

	g, err := database.Exec("INSERT INTO groups(name) VALUES ('工作')")
	if err != nil {
		t.Fatal(err)
	}
	gid, _ := g.LastInsertId()
	target := got[0].ID
	yes := true
	if err := UpdateMeta(database, target, MetaPatch{GroupID: &gid, Favorite: &yes}); err != nil {
		t.Fatal(err)
	}
	hit, err := Query(database, "", Filter{GroupID: &gid, Favorite: &yes}, DefaultPage)
	if err != nil {
		t.Fatal(err)
	}
	if len(hit) != 1 || hit[0].ID != target {
		t.Fatalf("分组+收藏过滤：%+v", hit)
	}

	// 敏感过滤
	sensitive := true
	if _, err := Upsert(database, model.NewText("敏感内容").WithSensitive(true)); err != nil {
		t.Fatal(err)
	}
	no := false
	clean, err := Query(database, "", Filter{Sensitive: &no}, DefaultPage)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range clean {
		if item.Sensitive {
			t.Fatalf("敏感过滤失效：%+v", item)
		}
	}
	only, _ := Query(database, "", Filter{Sensitive: &sensitive}, DefaultPage)
	if len(only) != 1 || !only[0].Sensitive {
		t.Fatalf("只查敏感：%+v", only)
	}
}

func TestPaginationIsStable(t *testing.T) {
	database := openTest(t)
	seed(t, database, 20)
	first, err := Query(database, "", Filter{}, Page{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Query(database, "", Filter{}, Page{Limit: 10, Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 10 || len(second) != 10 {
		t.Fatalf("分页条数 %d / %d", len(first), len(second))
	}
	var ids []int64
	for _, item := range append(first, second...) {
		ids = append(ids, item.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			t.Fatal("两页重叠")
		}
	}
}

// 1000 条假数据下的查询耗时（只打印，不做硬门槛，避免 CI 抖动）。
func TestThousandRowsTiming(t *testing.T) {
	database := openTest(t)
	seed(t, database, 1000)
	for _, q := range []string{"银河系", "hello", "银河"} {
		start := time.Now()
		got, err := Query(database, q, Filter{}, Page{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			t.Fatalf("%q 应有命中", q)
		}
		t.Logf("%q → %d 条，%s", q, len(got), time.Since(start))
	}
	start := time.Now()
	if got, err := Query(database, "", Filter{}, Page{Limit: 50}); err != nil || len(got) == 0 {
		t.Fatalf("空查询：%v", err)
	}
	t.Logf("空查询 %s", time.Since(start))
}
