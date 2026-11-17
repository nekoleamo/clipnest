// 查询与搜索。
//
// 契约 7：长词（≥3 个字符）走 FTS5（trigram 索引要求至少 3 个字符），
// 短词/单个汉字降级 LIKE；两者都只在 content 与 note 上匹配。
// 契约 8：置顶恒排最前，其后按 updated_at 倒序。
package db

import (
	"database/sql"
	"strings"

	"clipnest/internal/model"
)

// FTSMinChars 是走 FTS5 的最短查询长度（字符数）。
const FTSMinChars = 3

// Filter 是过滤条件；字段为零值表示该维度不过滤。
type Filter struct {
	Kinds     []model.ItemKind
	GroupID   *int64
	NoGroup   bool // true = 只要未分组的
	Favorite  *bool
	Pinned    *bool
	Sensitive *bool // nil = 不过滤；false = 排除敏感条目
}

// Page 是分页。
type Page struct {
	Limit  int64
	Offset int64
}

// DefaultPage 是默认分页。
var DefaultPage = Page{Limit: 200}

// Query 是只要条目的老入口（测试与一次性读取用）。
func Query(db *sql.DB, q string, filter Filter, page Page) ([]model.Item, error) {
	where, values := whereClause(q, filter)
	values = append(values, max0(page.Limit), max0(page.Offset))

	sqlText := "SELECT " + ItemColumns + " FROM items" + where +
		" ORDER BY pinned DESC, updated_at DESC, id DESC LIMIT ? OFFSET ?"
	rows, err := db.Query(sqlText, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.Item, 0, 32)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CountQuery 统计条数（分页用）；filter 语义同 Query。
func CountQuery(db *sql.DB, q string, filter Filter) (int64, error) {
	where, values := whereClause(q, filter)
	var n int64
	err := db.QueryRow("SELECT count(*) FROM items"+where, values...).Scan(&n)
	return n, err
}

// whereClause 拼 WHERE 与参数；搜索词为空时不限搜索维度。
func whereClause(q string, filter Filter) (string, []any) {
	needle := strings.TrimSpace(q)
	var parts []string
	var values []any

	if needle != "" {
		if len([]rune(needle)) >= FTSMinChars {
			// 整串当短语查，避免用户输入的 - * ( 被当成 FTS 语法
			parts = append(parts, "id IN (SELECT rowid FROM items_fts WHERE items_fts MATCH ?)")
			values = append(values, ftsPhrase(needle))
		} else {
			like := "%" + likeEscape(needle) + "%"
			parts = append(parts, `(content LIKE ? ESCAPE '\' OR ifnull(note, '') LIKE ? ESCAPE '\')`)
			values = append(values, like, like)
		}
	}
	if len(filter.Kinds) > 0 {
		marks := make([]string, len(filter.Kinds))
		for i, k := range filter.Kinds {
			marks[i] = "?"
			values = append(values, string(k))
		}
		parts = append(parts, "kind IN ("+strings.Join(marks, ",")+")")
	}
	if filter.GroupID != nil {
		parts = append(parts, "group_id = ?")
		values = append(values, *filter.GroupID)
	}
	if filter.NoGroup {
		parts = append(parts, "group_id IS NULL")
	}
	if filter.Favorite != nil {
		parts = append(parts, "favorite = ?")
		values = append(values, boolInt(*filter.Favorite))
	}
	if filter.Pinned != nil {
		parts = append(parts, "pinned = ?")
		values = append(values, boolInt(*filter.Pinned))
	}
	if filter.Sensitive != nil {
		parts = append(parts, "sensitive = ?")
		values = append(values, boolInt(*filter.Sensitive))
	}

	if len(parts) == 0 {
		return "", values
	}
	return " WHERE " + strings.Join(parts, " AND "), values
}

// ftsPhrase 把用户输入包成 FTS5 短语：内部双引号翻倍，其余字符全部失去语法含义。
func ftsPhrase(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}

// likeEscape 是 LIKE 的通配符转义（配合 ESCAPE '\'）。
func likeEscape(q string) string {
	var b strings.Builder
	b.Grow(len(q))
	for _, c := range q {
		switch c {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
