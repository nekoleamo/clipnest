// Package store 是采集结果落库：把剪贴板快照翻译成条目并写进数据库。
//
// 这一层只做「存」，不含 UI；面板读库走 internal/db 的 Query。
// 契约 3：文件条目的 content 是换行分隔的路径，file_types 是 d/f 串。
// 契约 4：图片文件名 = 图片内容的 blake3，原件存 images/origin、缩略图存 images/thumbnails。
package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"

	"clipnest/internal/db"
	"clipnest/internal/model"
	"clipnest/internal/paths"
)

// ThumbnailMax 是缩略图最长边（面板里显示用）。
const ThumbnailMax = 320

// ImageBytes 是图片 flavor（PNG 字节 + 像素尺寸）。
type ImageBytes struct {
	PNG           []byte
	Width, Height int64
}

// Captured 是一次采集里值得入库的内容。
type Captured struct {
	Text  string
	Files []string
	Image *ImageBytes
}

// IsEmpty 判断这次采集是否有内容。
func (c Captured) IsEmpty() bool {
	return c.Text == "" && len(c.Files) == 0 && c.Image == nil
}

// ToNewItems 翻译成要入库的条目。
//
// 文件优先：复制文件时系统同时给文本（路径字符串），只留文件条目。
func (c Captured) ToNewItems() []model.NewItem {
	if len(c.Files) > 0 {
		types := make([]byte, 0, len(c.Files))
		for _, p := range c.Files {
			types = append(types, fileTypeOf(p))
		}
		return []model.NewItem{model.NewFiles(c.Files, string(types))}
	}
	if c.Text != "" {
		return []model.NewItem{model.NewText(c.Text)}
	}
	return nil
}

// fileTypeOf 是 'd' 目录 / 'f' 文件（读不到时按文件算）。
func fileTypeOf(path string) byte {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return 'd'
	}
	return 'f'
}

// Stored 是一条已落库的记录。
type Stored struct {
	Kind   model.ItemKind
	Result db.UpsertResult
}

// Label 是日志用的 "INSERTED id=3" / "DEDUPED id=3"。
func (s Stored) Label() string {
	tag := "DEDUPED"
	if s.Result.Inserted {
		tag = "INSERTED"
	}
	return fmt.Sprintf("%s id=%d", tag, s.Result.ID)
}

// Store 是库连接 + 数据根。
type Store struct {
	Paths paths.Paths
	DB    *sql.DB
}

// Open 按数据根建好目录并打开库（首次启动自动建目录）。
func Open(p paths.Paths) (*Store, error) {
	if err := p.Ensure(); err != nil {
		return nil, fmt.Errorf("建数据目录: %w", err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		return nil, err
	}
	return &Store{Paths: p, DB: database}, nil
}

// OpenWithDB 用已有连接（测试用）；数据根自己保证目录存在。
func OpenWithDB(p paths.Paths, database *sql.DB) *Store {
	return &Store{Paths: p, DB: database}
}

// Close 关库。
func (s *Store) Close() error { return s.DB.Close() }

// Record 把快照写库；返回每条的结果。
//
// 图片优先：复制截图时系统同时给文本/位图 flavor，只留图片条目。
func (s *Store) Record(cap Captured) ([]Stored, error) {
	if cap.Image != nil {
		if item, err := s.saveImage(cap.Image); err == nil {
			result, err := db.Upsert(s.DB, item)
			if err != nil {
				return nil, err
			}
			return []Stored{{Kind: item.Kind, Result: result}}, nil
		}
	}
	out := make([]Stored, 0, len(cap.Files)+1)
	for _, item := range cap.ToNewItems() {
		result, err := db.Upsert(s.DB, item)
		if err != nil {
			return nil, err
		}
		out = append(out, Stored{Kind: item.Kind, Result: result})
	}
	return out, nil
}

// saveImage 把图片写进 images/origin 与 images/thumbnails，返回要入库的条目。
//
// 文件名就是图片内容的 blake3：同一张图再复制一次不会产生新文件。
func (s *Store) saveImage(img *ImageBytes) (model.NewItem, error) {
	hash := model.HashBytes(img.PNG)
	name := ImageFileName(hash)
	origin, thumb, err := s.Paths.EnsureImageDirs(hash)
	if err != nil {
		return model.NewItem{}, err
	}
	if _, err := os.Stat(origin); err != nil {
		if err := os.WriteFile(origin, img.PNG, 0o644); err != nil {
			return model.NewItem{}, err
		}
	}
	if _, err := os.Stat(thumb); err != nil {
		// 小图不缩：直接拷原图字节（重编码反而更大）。
		if maxOf(img.Width, img.Height) <= ThumbnailMax {
			if data, err := os.ReadFile(origin); err == nil {
				_ = os.WriteFile(thumb, data, 0o644)
			}
		} else if err := writeThumbnail(img.PNG, thumb); err != nil {
			// 缩略图失败不影响原图与入库（面板退化成显示尺寸）。
			logf("THUMB ERROR %v", err)
		}
	}
	return model.NewImage(name, img.Width, img.Height), nil
}

// ImageFileName 是契约 4：items.content 里存的图片文件名。
func ImageFileName(hash string) string { return hash + ".png" }

// writeThumbnail 生成缩略图（等比缩到 ThumbnailMax 以内）。
func writeThumbnail(pngBytes []byte, to string) error {
	src, _, err := image.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return errors.New("图片尺寸为 0")
	}
	scale := float64(ThumbnailMax) / float64(maxInt(w, h))
	dw, dh := int(float64(w)*scale), int(float64(h)*scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return err
	}
	return os.WriteFile(to, buf.Bytes(), 0o644)
}

// Remove 删一条：库里删掉，图片条目连 images/ 里的原图与缩略图一起删。
//
// 图片文件名就是内容哈希，所以同一张图只有一条记录、一组文件，删了不会误伤别的条目。
func (s *Store) Remove(id int64) (bool, error) {
	var content string
	err := s.DB.QueryRow("SELECT content FROM items WHERE id = ? AND kind = 'image'", id).Scan(&content)
	hasImage := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	deleted, err := db.Delete(s.DB, id)
	if err != nil || !deleted {
		return false, err
	}
	if hasImage {
		s.deleteImageFiles(content)
	}
	return true, nil
}

// RemoveKeepingFiles 批量删除，但把图片文件留着并返回它们的路径。
//
// 撤销窗口用：面板删条目后 10 秒内允许撤销，期间图片文件不能删（删了就恢复不出原图）。
// 调用方在窗口过去后调 DeleteImageFiles 真正回收。
func (s *Store) RemoveKeepingFiles(ids []int64) ([]db.RowData, []string, error) {
	snaps := make([]db.RowData, 0, len(ids))
	var files []string
	for _, id := range ids {
		snap, ok, err := db.DumpRow(s.DB, id)
		if err != nil {
			return snaps, files, err
		}
		if !ok {
			continue
		}
		if snap.Kind == string(model.KindImage) {
			hash := strings.TrimSuffix(snap.Content, ".png")
			files = append(files, s.Paths.ImageOrigin(hash), s.Paths.ImageThumbnail(hash))
		}
		// 与 Remove 不同：这里不删图片文件
		deleted, err := db.Delete(s.DB, id)
		if err != nil {
			return snaps, files, err
		}
		if deleted {
			snaps = append(snaps, snap)
		}
	}
	return snaps, files, nil
}

// DeleteImageFiles 真正回收一批图片文件（撤销窗口过后调用）。
func (s *Store) DeleteImageFiles(paths []string) {
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			logf("IMAGE delete failed %s: %v", p, err)
		}
	}
}

// RemoveMany 批量删除（图片资源连带回收），返回真正删掉的条数。
func (s *Store) RemoveMany(ids []int64) (int, error) {
	deleted := 0
	for _, id := range ids {
		ok, err := s.Remove(id)
		if err != nil {
			return deleted, err
		}
		if ok {
			deleted++
		}
	}
	return deleted, nil
}

// PruneOlderThan 删掉 updated_at 早于 cutoff（RFC3339）的条目；收藏与置顶受保护（契约 9）。
func (s *Store) PruneOlderThan(cutoff string) (int, error) {
	rows, err := s.DB.Query(`SELECT id FROM items
		WHERE updated_at < ? AND pinned = 0 AND favorite = 0
		ORDER BY updated_at`, cutoff)
	if err != nil {
		return 0, err
	}
	var victims []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		victims = append(victims, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	deleted := 0
	for _, id := range victims {
		ok, err := s.Remove(id)
		if err != nil {
			return deleted, err
		}
		if ok {
			deleted++
		}
	}
	if deleted > 0 {
		_, _ = s.CollectGarbage()
	}
	return deleted, nil
}

// ClearAll 清空全部历史（收藏与置顶也一并删掉——这是用户明确点的动作）。
//
// 图片文件逐条删，最后再扫一次孤儿。
func (s *Store) ClearAll() (int, error) {
	rows, err := s.DB.Query("SELECT id FROM items")
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	deleted := 0
	for _, id := range ids {
		ok, err := s.Remove(id)
		if err != nil {
			return deleted, err
		}
		if ok {
			deleted++
		}
	}
	_, _ = s.CollectGarbage()
	return deleted, nil
}

// deleteImageFiles 删掉某个图片条目的原图与缩略图（文件不在就跳过）。
func (s *Store) deleteImageFiles(content string) {
	hash := strings.TrimSuffix(content, ".png")
	for _, path := range []string{s.Paths.ImageOrigin(hash), s.Paths.ImageThumbnail(hash)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			logf("IMAGE delete failed %s: %v", path, err)
		}
	}
}

// CollectGarbage 扫 images/ 里没有对应条目的孤儿文件（历史遗留、导入失败留下的）并删掉。
//
// 返回删除的文件数。启动时跑一次就够了，成本是一次目录遍历。
func (s *Store) CollectGarbage() (int, error) {
	rows, err := s.DB.Query("SELECT content FROM items WHERE kind = 'image'")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	wanted := make(map[string]struct{})
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return 0, err
		}
		wanted[strings.TrimSuffix(content, ".png")] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	removed := 0
	for _, dir := range []string{"origin", "thumbnails"} {
		root := filepath.Join(s.Paths.ImagesDir(), dir)
		shards, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, shard := range shards {
			if !shard.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(root, shard.Name()))
			if err != nil {
				continue
			}
			for _, file := range files {
				name := file.Name()
				if _, ok := wanted[strings.TrimSuffix(name, ".png")]; ok {
					continue
				}
				if err := os.Remove(filepath.Join(root, shard.Name(), name)); err == nil {
					removed++
				}
			}
		}
	}
	return removed, nil
}

// Prune 是契约 9：按上限裁剪历史，收藏与置顶永远保留。
//
// 只删「既不收藏也不置顶」里最旧的那些，maxItems 是总条数上限，
// 受保护条目先把名额占掉。返回删除条数。
func (s *Store) Prune(maxItems int64) (int, error) {
	if maxItems <= 0 {
		return 0, nil // 0 = 不限
	}
	var protected int64
	if err := s.DB.QueryRow("SELECT count(*) FROM items WHERE pinned = 1 OR favorite = 1").Scan(&protected); err != nil {
		return 0, err
	}
	keep := maxItems - protected
	if keep < 0 {
		keep = 0
	}

	rows, err := s.DB.Query(`SELECT id FROM items
		WHERE pinned = 0 AND favorite = 0
		ORDER BY updated_at DESC, id DESC
		LIMIT -1 OFFSET ?`, keep)
	if err != nil {
		return 0, err
	}
	var victims []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		victims = append(victims, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	deleted := 0
	for _, id := range victims {
		ok, err := s.Remove(id)
		if err != nil {
			return deleted, err
		}
		if ok {
			deleted++
		}
	}
	return deleted, nil
}

// Count 是库里现有条目数，供启动日志与面板显示。
func (s *Store) Count() (int64, error) { return db.Count(s.DB) }

func maxOf(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Logf 是日志出口，宿主可替换（默认丢弃，测试不吵）。
var Logf = func(format string, args ...any) {}

func logf(format string, args ...any) { Logf(format, args...) }

// WriteThumbnail 生成缩略图（同步拉回图片后重建用）。
func WriteThumbnail(pngBytes []byte, to string) error { return writeThumbnail(pngBytes, to) }
