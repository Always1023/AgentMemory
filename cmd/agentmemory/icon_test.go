package main

import (
	"encoding/binary"
	"image/png"
	"os"
	"testing"
)

// 产品图标：主图 1024×1024，ICO 含 Windows 常用的六档（I-071）。
func TestProductIconsAreComplete(t *testing.T) {
	f, err := os.Open("build/appicon.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatalf("解码 build/appicon.png: %v", err)
	}
	if cfg.Width != 1024 || cfg.Height != 1024 {
		t.Errorf("主图标应当是 1024×1024，实际 %d×%d", cfg.Width, cfg.Height)
	}

	raw, err := os.ReadFile("build/windows/icon.ico")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 6 {
		t.Fatalf("icon.ico 太短：%d 字节", len(raw))
	}
	if got := binary.LittleEndian.Uint16(raw[2:4]); got != 1 {
		t.Errorf("icon.ico 的类型应当是 1（图标），实际 %d", got)
	}
	count := int(binary.LittleEndian.Uint16(raw[4:6]))
	if len(raw) < 6+16*count {
		t.Fatalf("icon.ico 的目录不完整：count=%d len=%d", count, len(raw))
	}
	sizes := map[int]bool{}
	for i := 0; i < count; i++ {
		w := int(raw[6+16*i])
		if w == 0 {
			w = 256
		}
		sizes[w] = true
	}
	for _, want := range []int{256, 128, 64, 48, 32, 16} {
		if !sizes[want] {
			t.Errorf("icon.ico 缺 %d×%d 这一档，实际 %v", want, want, sizes)
		}
	}
}
