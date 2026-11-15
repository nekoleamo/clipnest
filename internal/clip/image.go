package clip

import (
	"bytes"
	"image"
	_ "image/gif"  // 剪贴板偶尔给 GIF
	_ "image/jpeg" // 以及 JPEG
	_ "image/png"
)

// imageSize 读图片像素尺寸；解不出就返回 error（调用方只记 flavor 不落库）。
func imageSize(data []byte) (int64, int64, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	return int64(cfg.Width), int64(cfg.Height), nil
}
