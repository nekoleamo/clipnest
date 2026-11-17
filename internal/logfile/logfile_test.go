package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotatesWhenFull 写满上限就轮转成 .1，新的继续从空文件写。
func TestRotatesWhenFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := Open(path, 1<<10)
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	defer w.Close()

	line := strings.Repeat("x", 100) + "\n"
	for i := 0; i < 30; i++ { // 3KB，必然翻过 1KB
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("写入失败：%v", err)
		}
	}

	cur, err := os.Stat(path)
	if err != nil {
		t.Fatalf("当前日志不存在：%v", err)
	}
	if cur.Size() > 1<<10 {
		t.Fatalf("当前文件超过上限：%d", cur.Size())
	}
	old, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("没有轮转出 .1：%v", err)
	}
	if old.Size() == 0 {
		t.Fatal(".1 是空文件，轮转没把内容搬过去")
	}
}

// TestOpenRotatesOversize 启动时文件已超大也要先轮转（长跑进程重启场景）。
func TestOpenRotatesOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("y", 2048)), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path, 1<<10)
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}

	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(cur) != "after\n" {
		t.Fatalf("打开后应当写进新文件，实际 %q", string(cur))
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("超大的旧文件应当轮转成 .1：%v", err)
	}
}
