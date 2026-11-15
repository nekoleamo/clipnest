package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"clipnest/internal/paths"
)

func tempPaths(t *testing.T, tag string) paths.Paths {
	t.Helper()
	return paths.FromRoot(filepath.Join(t.TempDir(), tag))
}

func TestMissingFileGivesDefaults(t *testing.T) {
	if got := Load(tempPaths(t, "missing")); !reflect.DeepEqual(got, Default()) {
		t.Fatalf("得 %+v", got)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	p := tempPaths(t, "roundtrip")
	want := Default()
	want.MaxItems = 123
	want.PanelHeight = 600
	if err := Save(want, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Settings()); err != nil {
		t.Fatalf("设置文件没写出来：%v", err)
	}
	if got := Load(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("往返失败：%+v", got)
	}
	// 不留临时文件
	if _, err := os.Stat(p.Settings() + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("残留临时文件")
	}
}

func TestBrokenOrPartialFilesFallBackOrFillIn(t *testing.T) {
	p := tempPaths(t, "broken")
	if err := os.MkdirAll(p.Root(), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p.Settings(), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(p); !reflect.DeepEqual(got, Default()) {
		t.Fatal("坏文件应用默认值")
	}

	// 只写一个字段：其余用默认值
	if err := os.WriteFile(p.Settings(), []byte(`{"max_items": 7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load(p)
	if got.MaxItems != 7 {
		t.Fatalf("max_items=%d", got.MaxItems)
	}
	if got.PanelHeight != Default().PanelHeight {
		t.Fatalf("未写字段应保持默认：%d", got.PanelHeight)
	}
}

func TestOutOfRangeValuesAreClamped(t *testing.T) {
	s := Default()
	s.MaxItems = -5
	s.PanelHeight = 99_999
	s = Normalize(s)
	if s.MaxItems != 0 || s.PanelHeight != PanelHeightMax {
		t.Fatalf("上限裁剪：%+v", s)
	}
	low := Normalize(Settings{PanelHeight: 1, PanelPosition: "weird"})
	if low.PanelHeight != PanelHeightMin || low.PanelPosition != PositionCenter {
		t.Fatalf("下限裁剪：%+v", low)
	}
}
