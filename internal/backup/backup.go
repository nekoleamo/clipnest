// Package backup 是加密备份（自有格式 v1）。
//
// 容器字节布局：
//
//		"CNST1" (5) | salt (16) | nonce (12) | ciphertext + tag
//
//	  - key = argon2id(passphrase, salt, 32B)（m=19 MiB、t=2、p=1，与旧实现一致）
//	  - 明文是 JSON（Payload：版本 + 全部 items/groups）
//	  - AAD 绑定 magic，格式头被改过会解密失败
//
// 旧实现（src/backup.rs，已从仓库删除）的字节布局在这里原样保留（契约 11）：
// 用旧版导出的 .cnbak 必须能被新版打开。
package backup

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"clipnest/internal/paths"
)

// 容器参数。
const (
	SaltLen  = 16
	NonceLen = 12
	KeyLen   = 32
)

// Magic 是容器头，同时作为 AEAD 的 AAD。
var Magic = []byte("CNST1")

// FormatVersion 是明文 JSON 的格式版本。
const FormatVersion = 1

// argon2id 参数（与 Rust 的 Argon2::default() 对齐）。
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
)

// 错误。
var (
	ErrCrypto = errors.New("口令错误，或备份文件已损坏")
	ErrFormat = errors.New("备份格式不支持")
)

// Item 是备份里的条目（字段名与旧实现一致）。
type Item struct {
	ID          int64   `json:"id"`
	ContentHash string  `json:"content_hash"`
	Kind        string  `json:"kind"`
	Content     string  `json:"content"`
	FileTypes   *string `json:"file_types"`
	Width       *int64  `json:"width"`
	Height      *int64  `json:"height"`
	UseCount    int64   `json:"use_count"`
	Pinned      bool    `json:"pinned"`
	Favorite    bool    `json:"favorite"`
	Note        *string `json:"note"`
	GroupID     *int64  `json:"group_id"`
	Sensitive   bool    `json:"sensitive"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// Group 是备份里的分组。
type Group struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Sort int64  `json:"sort"`
}

// Payload 是备份里的明文内容。
type Payload struct {
	Format    int     `json:"format"`
	App       string  `json:"app"`
	CreatedAt string  `json:"created_at"`
	Items     []Item  `json:"items"`
	Groups    []Group `json:"groups"`
}

// ImportReport 是导入结果。
type ImportReport struct {
	Inserted       int
	Skipped        int
	GroupsInserted int
}

// Seal 加密一段明文，返回容器字节（magic + salt + nonce + ciphertext）。
func Seal(plain []byte, passphrase string) ([]byte, error) {
	salt := make([]byte, SaltLen)
	nonceBytes := make([]byte, NonceLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	key := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, KeyLen)
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nil, nonceBytes, plain, Magic)

	out := make([]byte, 0, len(Magic)+SaltLen+NonceLen+len(ciphertext))
	out = append(out, Magic...)
	out = append(out, salt...)
	out = append(out, nonceBytes...)
	out = append(out, ciphertext...)
	return out, nil
}

// Open 解密一个容器；口令错 / 文件被改都返回 ErrCrypto。
func Open(blob []byte, passphrase string) ([]byte, error) {
	head := len(Magic) + SaltLen + NonceLen
	if len(blob) <= head || string(blob[:len(Magic)]) != string(Magic) {
		return nil, fmt.Errorf("%w：不是 clipnest 备份文件", ErrFormat)
	}
	salt := blob[len(Magic) : len(Magic)+SaltLen]
	nonceBytes := blob[len(Magic)+SaltLen : head]
	ciphertext := blob[head:]

	key := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, KeyLen)
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonceBytes, ciphertext, Magic)
	if err != nil {
		return nil, ErrCrypto
	}
	return plain, nil
}

// Sealer 返回一对编解码器（同步载荷用），语义与旧实现的 backup::sealer 相同。
func Sealer(passphrase string) (seal func([]byte) ([]byte, error), open func([]byte) ([]byte, error)) {
	return func(plain []byte) ([]byte, error) { return Seal(plain, passphrase) },
		func(blob []byte) ([]byte, error) { return Open(blob, passphrase) }
}

// Plain 是不加密的编解码（本地目录同步用）。
func Plain(b []byte) ([]byte, error) { return append([]byte(nil), b...), nil }

// DefaultName 是备份文件名：clipnest-<YYYYmmdd-HHMMSS>.cnbak。
func DefaultName() string {
	return "clipnest-" + time.Now().Format("20060102-150405") + ".cnbak"
}

// ExportTo 导出到数据目录的 backups/ 下，返回落盘路径。
func ExportTo(db *sql.DB, p paths.Paths, passphrase string) (string, error) {
	path := p.Backup(DefaultName())
	if err := Export(db, path, passphrase); err != nil {
		return "", err
	}
	return path, nil
}

// Export 导出整库到 path（先写临时文件再改名）。
func Export(db *sql.DB, path, passphrase string) error {
	blob, err := ExportBytes(db, passphrase)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ExportBytes 导出并返回容器字节。
func ExportBytes(db *sql.DB, passphrase string) ([]byte, error) {
	payload, err := collect(db)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return Seal(plain, passphrase)
}

// collect 把整库读成 Payload。
func collect(db *sql.DB) (Payload, error) {
	out := Payload{Format: FormatVersion, App: "clipnest", CreatedAt: now()}

	rows, err := db.Query(`SELECT id, content_hash, kind, content, file_types, width, height,
		use_count, pinned, favorite, note, group_id, sensitive, created_at, updated_at FROM items ORDER BY id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			item                   Item
			fileTypes, note        sql.NullString
			width, height, groupID sql.NullInt64
			pinned, favorite, sens int64
		)
		if err := rows.Scan(&item.ID, &item.ContentHash, &item.Kind, &item.Content, &fileTypes,
			&width, &height, &item.UseCount, &pinned, &favorite, &note, &groupID, &sens,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return out, err
		}
		if fileTypes.Valid {
			item.FileTypes = &fileTypes.String
		}
		if width.Valid {
			item.Width = &width.Int64
		}
		if height.Valid {
			item.Height = &height.Int64
		}
		if note.Valid {
			item.Note = &note.String
		}
		if groupID.Valid {
			item.GroupID = &groupID.Int64
		}
		item.Pinned, item.Favorite, item.Sensitive = pinned != 0, favorite != 0, sens != 0
		out.Items = append(out.Items, item)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	groups, err := db.Query("SELECT id, name, sort FROM groups ORDER BY id")
	if err != nil {
		return out, err
	}
	defer groups.Close()
	for groups.Next() {
		var g Group
		if err := groups.Scan(&g.ID, &g.Name, &g.Sort); err != nil {
			return out, err
		}
		out.Groups = append(out.Groups, g)
	}
	return out, groups.Err()
}

// Import 从 path 导入：只补齐缺失条目，不覆盖本地已有的同哈希条目。
func Import(db *sql.DB, path, passphrase string) (ImportReport, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return ImportReport{}, err
	}
	return ImportBytes(db, blob, passphrase)
}

// ImportBytes 从容器字节导入。
func ImportBytes(db *sql.DB, blob []byte, passphrase string) (ImportReport, error) {
	plain, err := Open(blob, passphrase)
	if err != nil {
		return ImportReport{}, err
	}
	var payload Payload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return ImportReport{}, fmt.Errorf("备份内容损坏：%w", err)
	}
	if payload.Format != FormatVersion {
		return ImportReport{}, fmt.Errorf("%w：版本 %d", ErrFormat, payload.Format)
	}

	tx, err := db.Begin()
	if err != nil {
		return ImportReport{}, err
	}
	defer tx.Rollback()

	var report ImportReport
	// 分组按名字对齐（id 是各自的自增值）
	groupMap := map[int64]int64{}
	for _, g := range payload.Groups {
		if _, err := tx.Exec("INSERT INTO groups(name, sort) VALUES (?, ?) ON CONFLICT(name) DO NOTHING", g.Name, g.Sort); err != nil {
			return report, err
		}
		var id int64
		if err := tx.QueryRow("SELECT id FROM groups WHERE name = ?", g.Name).Scan(&id); err != nil {
			return report, err
		}
		groupMap[g.ID] = id
		report.GroupsInserted++
	}

	for _, item := range payload.Items {
		var exists int64
		err := tx.QueryRow("SELECT count(*) FROM items WHERE content_hash = ?", item.ContentHash).Scan(&exists)
		if err != nil {
			return report, err
		}
		if exists > 0 {
			report.Skipped++
			continue
		}
		var groupID any
		if item.GroupID != nil {
			if mapped, ok := groupMap[*item.GroupID]; ok {
				groupID = mapped
			}
		}
		if _, err := tx.Exec(`INSERT INTO items(content_hash, kind, content, file_types, width, height,
				use_count, pinned, favorite, note, group_id, sensitive, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ContentHash, item.Kind, item.Content, item.FileTypes, item.Width, item.Height,
			item.UseCount, boolToInt(item.Pinned), boolToInt(item.Favorite), item.Note, groupID,
			boolToInt(item.Sensitive), item.CreatedAt, item.UpdatedAt); err != nil {
			return report, err
		}
		report.Inserted++
	}

	if err := tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func boolToInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
