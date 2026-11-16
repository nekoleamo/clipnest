// 生成应用图标 resources/icon.png（1024×1024，2 倍超采样后降采样，自带抗锯齿）。
//
//	go run ./tools/icongen
//
// mygo build 默认读 resources/icon.png，会自动转成 .icns 打进 .app。
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

const (
	finalSize = 1024
	scale     = 2 // 超采样倍数
	size      = finalSize * scale
)

// 背景渐变两端（靛蓝 → 紫罗兰），与 macOS 深色模式都好搭。
var (
	bgTop    = color.RGBA{0x4F, 0x46, 0xE5, 0xFF}
	bgBottom = color.RGBA{0x7C, 0x3A, 0xED, 0xFF}
	paper    = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	ink      = color.RGBA{0x4F, 0x46, 0xE5, 0xFF}
)

func main() {
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// 1) 圆角背景 + 竖直渐变
	radius := float64(size) * 0.225
	for y := 0; y < size; y++ {
		t := float64(y) / float64(size-1)
		bg := lerpColor(bgTop, bgBottom, t)
		for x := 0; x < size; x++ {
			if insideRoundedRect(float64(x), float64(y), 0, 0, float64(size), float64(size), radius) {
				img.SetRGBA(x, y, bg)
			}
		}
	}

	// 2) 剪贴板：白色板子 + 顶部夹子 + 三条内容线
	boardW := float64(size) * 0.46
	boardH := float64(size) * 0.56
	boardX := (float64(size) - boardW) / 2
	boardY := (float64(size)-boardH)/2 + float64(size)*0.02
	boardR := float64(size) * 0.075

	fillRoundedRect(img, boardX, boardY, boardW, boardH, boardR, paper)

	// 夹子：骑在板子上沿
	clipW := float64(size) * 0.20
	clipH := float64(size) * 0.085
	clipX := (float64(size) - clipW) / 2
	clipY := boardY - clipH/2
	fillRoundedRect(img, clipX, clipY, clipW, clipH, clipH/2, paper)
	// 夹子里挖个小口，看起来有层次
	fillRoundedRect(img, clipX+clipW*0.28, clipY+clipH*0.30, clipW*0.44, clipH*0.40, clipH*0.2, bgTop)

	// 三条内容线（第二条短一点，像真列表）
	lineH := float64(size) * 0.035
	lineX := boardX + boardW*0.16
	widths := []float64{0.68, 0.46, 0.58}
	for i, w := range widths {
		y := boardY + boardH*(0.40+0.16*float64(i))
		fillRoundedRect(img, lineX, y, boardW*w, lineH, lineH/2, ink)
	}

	// 3) 降采样
	out := downsample(img, scale)
	if err := os.MkdirAll("resources", 0o755); err != nil {
		log.Fatal(err)
	}
	path := filepath.Join("resources", "icon.png")
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	if err := png.Encode(f, out); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	log.Printf("已生成 %s（%d×%d）", path, finalSize, finalSize)
}

func lerpColor(a, b color.RGBA, t float64) color.RGBA {
	mix := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xFF}
}

// insideRoundedRect 判断点是否在圆角矩形内（逐像素填充用）。
func insideRoundedRect(x, y, rx, ry, w, h, r float64) bool {
	cx := math.Max(rx+r, math.Min(x, rx+w-r))
	cy := math.Max(ry+r, math.Min(y, ry+h-r))
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r || (x >= rx+r && x <= rx+w-r && y >= ry && y <= ry+h) ||
		(y >= ry+r && y <= ry+h-r && x >= rx && x <= rx+w)
}

// fillRoundedRect 用一个比矩形稍大的圆角形状填充（含 1 像素保底，避免细线漏空）。
func fillRoundedRect(img *image.RGBA, x, y, w, h, r float64, c color.RGBA) {
	for py := int(math.Floor(y)) - 1; py <= int(math.Ceil(y+h))+1; py++ {
		for px := int(math.Floor(x)) - 1; px <= int(math.Ceil(x+w))+1; px++ {
			if px < 0 || py < 0 || px >= size || py >= size {
				continue
			}
			if insideRoundedRect(float64(px)+0.5, float64(py)+0.5, x, y, w, h, r) {
				img.SetRGBA(px, py, c)
			}
		}
	}
}

// downsample 按整数倍做盒式降采样（抗锯齿）。
func downsample(src *image.RGBA, k int) *image.RGBA {
	w := src.Bounds().Dx() / k
	h := src.Bounds().Dy() / k
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b, a uint32
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					cr, cg, cb, ca := src.At(x*k+dx, y*k+dy).RGBA()
					r += cr
					g += cg
					b += cb
					a += ca
				}
			}
			n := uint32(k * k)
			// RGBA() 返回的是 16 位，转回 8 位
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(b / n >> 8), uint8(a / n >> 8)})
		}
	}
	return dst
}
