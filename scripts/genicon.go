//go:build ignore

// Generates every brand asset in the repo from a single transparent source image.
//
// Source: assets/brand/logo-source.png  (a real PNG with a usable alpha channel)
//
// Outputs:
//
//	assets/brand/icon.ico            multi-size Windows icon (application, title bar, tray)
//	assets/brand/icon-<size>.png     common PNG sizes
//	assets/brand/logo.png            full-size logo for docs / README
//	agent/gui/assets/icon.ico        icon embedded in the desktop GUI binary
//	agent/gui/assets/icon.png        logo shown inside the desktop GUI
//	server/web/favicon.ico           Admin console favicon
//	server/web/img/logo.png          Admin console sidebar logo
//	server/web/img/logo-32.png       Admin console small logo
//	server/web/apple-touch-icon.png
//
// Run with:  go run scripts/genicon.go [repoRoot]
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	_ "image/jpeg" // tolerate a JPEG masquerading as .png, and report it clearly
)

// iconSizes covers every size Windows actually asks for: tray (16/20/24),
// taskbar and alt-tab (32/40/48), Explorer tiles (64/128) and the
// high-resolution shell preview (256).
var iconSizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	srcPath := filepath.Join(root, "assets", "brand", "logo-source.png")

	src, format, err := decode(srcPath)
	if err != nil {
		fatal(err)
	}
	b := src.Bounds()
	fmt.Printf("source: %s %dx%d\n", format, b.Dx(), b.Dy())

	// A transparent source is mandatory: an opaque PNG/JPEG here is exactly how
	// the app icon previously ended up as a black square in the taskbar.
	opaqueShare, transparentShare := alphaShares(src)
	fmt.Printf("alpha: transparent=%.1f%% opaque=%.1f%%\n", transparentShare*100, opaqueShare*100)
	if format == "jpeg" {
		fatal(fmt.Errorf("%s is a JPEG, not a PNG — replace it with a transparent PNG", srcPath))
	}
	if transparentShare < 0.05 {
		fatal(fmt.Errorf("%s has no usable transparency (only %.1f%% of pixels are transparent); the icon would ship with an opaque background", srcPath, transparentShare*100))
	}

	glyph, contentW, contentH := trimToContent(src)
	if glyph == nil {
		fatal(fmt.Errorf("%s appears to be fully transparent", srcPath))
	}
	gb := glyph.Bounds()
	fmt.Printf("content: trimmed %dx%d -> padded square %dx%d\n", contentW, contentH, gb.Dx(), gb.Dy())

	brandDir := filepath.Join(root, "assets", "brand")
	guiAssets := filepath.Join(root, "agent", "gui", "assets")
	webDir := filepath.Join(root, "server", "web")
	webImg := filepath.Join(webDir, "img")
	for _, d := range []string{brandDir, guiAssets, webImg} {
		mustMkdir(d)
	}

	// --- Multi-size ICO ---
	ico, err := buildICO(glyph, iconSizes)
	if err != nil {
		fatal(err)
	}
	for _, p := range []string{
		filepath.Join(brandDir, "icon.ico"),
		filepath.Join(guiAssets, "icon.ico"),
		filepath.Join(webDir, "favicon.ico"),
	} {
		writeFile(p, ico)
	}

	// --- PNG sizes ---
	for _, s := range iconSizes {
		writePNG(filepath.Join(brandDir, fmt.Sprintf("icon-%d.png", s)), resizeBox(glyph, s))
	}

	// --- Full-size logo keeps the original framing, not the tight crop ---
	writePNG(filepath.Join(brandDir, "logo.png"), toRGBA(src))

	// --- Desktop GUI assets ---
	writePNG(filepath.Join(guiAssets, "icon.png"), resizeBox(glyph, 256))

	// --- Admin console assets ---
	writePNG(filepath.Join(webImg, "logo.png"), resizeBox(glyph, 256))
	writePNG(filepath.Join(webImg, "logo-32.png"), resizeBox(glyph, 32))
	writePNG(filepath.Join(webDir, "apple-touch-icon.png"), resizeBox(glyph, 256))

	fmt.Println("done")
}

func decode(path string) (image.Image, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		return nil, "", fmt.Errorf("decode %s: %w", path, err)
	}
	return img, format, nil
}

func alphaShares(img image.Image) (opaque, transparent float64) {
	b := img.Bounds()
	total := b.Dx() * b.Dy()
	if total == 0 {
		return 0, 0
	}
	var nOpaque, nTransparent int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			switch {
			case a == 0:
				nTransparent++
			case a == 0xffff:
				nOpaque++
			}
		}
	}
	return float64(nOpaque) / float64(total), float64(nTransparent) / float64(total)
}

// glyphAlpha is the minimum alpha that counts as content, so the low-alpha haze
// some image generators leave around a mark does not bloat the trim box.
const glyphAlpha = 0x1800

// trimToContent centres the visible mark on a transparent square canvas. Rows
// and columns are measured by accumulated alpha rather than by a raw max-alpha
// box: a single stray speck then cannot drag the crop out to the image edge.
// It returns the square canvas plus the trimmed content dimensions.
func trimToContent(src image.Image) (image.Image, int, int) {
	sb := src.Bounds()
	w, h := sb.Dx(), sb.Dy()
	if w == 0 || h == 0 {
		return nil, 0, 0
	}

	rows := make([]uint64, h)
	cols := make([]uint64, w)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			_, _, _, a := src.At(sb.Min.X+x, sb.Min.Y+y).RGBA()
			if a < glyphAlpha {
				continue
			}
			rows[y] += uint64(a)
			cols[x] += uint64(a)
		}
	}

	minX, maxX := spanOf(cols)
	minY, maxY := spanOf(rows)
	if minX < 0 || minY < 0 {
		return nil, 0, 0
	}

	bw, bh := maxX-minX+1, maxY-minY+1
	side := bw
	if bh > side {
		side = bh
	}
	// Keep only a small safety margin. Windows notification icons are often
	// rendered at 16-24px, where the old 8% margin made the visible mark look
	// noticeably undersized.
	pad := side * 2 / 100
	if pad < 1 {
		pad = 1
	}
	side += pad * 2

	// Draw the content box centred onto a fresh square canvas: the padding is
	// real transparent margin, and no clamping against the source edge is needed.
	square := image.NewRGBA(image.Rect(0, 0, side, side))
	content := image.Rect(sb.Min.X+minX, sb.Min.Y+minY, sb.Min.X+maxX+1, sb.Min.Y+maxY+1)
	dst := image.Rect((side-bw)/2, (side-bh)/2, (side-bw)/2+bw, (side-bh)/2+bh)
	draw.Draw(square, dst, src, content.Min, draw.Src)
	cleanAlpha(square)
	return square, bw, bh
}

// spanOf returns the first and last index whose alpha mass clears 1% of the
// busiest row or column.
func spanOf(mass []uint64) (int, int) {
	var peak uint64
	for _, m := range mass {
		if m > peak {
			peak = m
		}
	}
	if peak == 0 {
		return -1, -1
	}
	threshold := peak / 100
	if threshold == 0 {
		threshold = 1
	}
	first, last := -1, -1
	for i, m := range mass {
		if m <= threshold {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	return first, last
}

// cleanAlpha drops near-transparent haze so 16px renders stay crisp.
func cleanAlpha(img *image.RGBA) {
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i+3] < 10 {
			img.Pix[i+3] = 0
		}
	}
}

// resizeBox downscales with area averaging: a plain bilinear filter turns a
// 16px render into mush and drops the thin strokes of the mark.
func resizeBox(src image.Image, size int) *image.RGBA {
	sb := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	if sb.Dx() == size && sb.Dy() == size {
		draw.Draw(dst, dst.Bounds(), src, sb.Min, draw.Src)
		return dst
	}

	scaleX := float64(sb.Dx()) / float64(size)
	scaleY := float64(sb.Dy()) / float64(size)
	for y := 0; y < size; y++ {
		y0 := sb.Min.Y + int(float64(y)*scaleY)
		y1 := sb.Min.Y + int(float64(y+1)*scaleY)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		if y1 > sb.Max.Y {
			y1 = sb.Max.Y
		}
		for x := 0; x < size; x++ {
			x0 := sb.Min.X + int(float64(x)*scaleX)
			x1 := sb.Min.X + int(float64(x+1)*scaleX)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if x1 > sb.Max.X {
				x1 = sb.Max.X
			}

			// Average in premultiplied space, otherwise the colour of fully
			// transparent pixels bleeds in and rings the mark with black.
			var sumR, sumG, sumB, sumA, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					pr, pg, pb, pa := src.At(sx, sy).RGBA()
					sumR += uint64(pr) * uint64(pa) / 0xffff
					sumG += uint64(pg) * uint64(pa) / 0xffff
					sumB += uint64(pb) * uint64(pa) / 0xffff
					sumA += uint64(pa)
					n++
				}
			}
			o := dst.PixOffset(x, y)
			if n == 0 {
				continue
			}
			avgA := sumA / n
			if avgA == 0 {
				continue // leave fully transparent
			}
			// Un-premultiply back to straight alpha before writing the byte.
			dst.Pix[o+0] = unPremultiply(sumR/n, avgA)
			dst.Pix[o+1] = unPremultiply(sumG/n, avgA)
			dst.Pix[o+2] = unPremultiply(sumB/n, avgA)
			dst.Pix[o+3] = uint8(avgA >> 8)
		}
	}
	return dst
}

// unPremultiply converts a premultiplied 16-bit channel plus its alpha back to
// an 8-bit straight-alpha channel, clamped to a valid byte range.
func unPremultiply(premul, alpha uint64) uint8 {
	if alpha == 0 {
		return 0
	}
	v := premul * 0xffff / alpha
	if v > 0xffff {
		v = 0xffff
	}
	return uint8(v >> 8)
}

func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// encodeICODIB writes a classic 32-bit BGRA DIB frame. LoadImage and
// Shell_NotifyIcon still mishandle PNG-compressed frames below 256px, so only
// the 256px frame is stored as PNG.
func encodeICODIB(img *image.RGBA) []byte {
	size := img.Bounds().Dx()
	xorStride := size * 4
	andStride := (size + 31) / 32 * 4
	const headerSize = 40
	buf := make([]byte, headerSize+xorStride*size+andStride*size)

	binary.LittleEndian.PutUint32(buf[0:4], 40)              // biSize
	binary.LittleEndian.PutUint32(buf[4:8], uint32(size))    // biWidth
	binary.LittleEndian.PutUint32(buf[8:12], uint32(size*2)) // biHeight (XOR + AND)
	binary.LittleEndian.PutUint16(buf[12:14], 1)             // biPlanes
	binary.LittleEndian.PutUint16(buf[14:16], 32)            // biBitCount
	binary.LittleEndian.PutUint32(buf[20:24], uint32(xorStride*size+andStride*size))

	// The XOR bitmap is bottom-up BGRA. The AND mask stays zero: with 32bpp
	// frames Windows uses the per-pixel alpha from the XOR layer.
	for y := 0; y < size; y++ {
		src := img.Pix[y*img.Stride : y*img.Stride+xorStride]
		dst := buf[headerSize+(size-1-y)*xorStride:]
		for x := 0; x < size; x++ {
			dst[x*4+0] = src[x*4+2]
			dst[x*4+1] = src[x*4+1]
			dst[x*4+2] = src[x*4+0]
			dst[x*4+3] = src[x*4+3]
		}
	}
	return buf
}

func encodeIconFrame(src image.Image, size int) ([]byte, error) {
	img := resizeBox(src, size)
	if size < 256 {
		return encodeICODIB(img), nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildICO(src image.Image, sizes []int) ([]byte, error) {
	images := make([][]byte, 0, len(sizes))
	for _, size := range sizes {
		payload, err := encodeIconFrame(src, size)
		if err != nil {
			return nil, err
		}
		images = append(images, payload)
	}

	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0)) // reserved
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // type = icon
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(sizes)))

	offset := uint32(6 + 16*len(sizes))
	for i, size := range sizes {
		var w, h uint8
		if size >= 256 {
			w, h = 0, 0 // 0 means 256 in the ICO directory
		} else {
			w, h = uint8(size), uint8(size)
		}
		_ = buf.WriteByte(w)
		_ = buf.WriteByte(h)
		_ = buf.WriteByte(0)                                    // colour count
		_ = buf.WriteByte(0)                                    // reserved
		_ = binary.Write(&buf, binary.LittleEndian, uint16(1))  // planes
		_ = binary.Write(&buf, binary.LittleEndian, uint16(32)) // bit count
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(images[i])))
		_ = binary.Write(&buf, binary.LittleEndian, offset)
		offset += uint32(len(images[i]))
	}
	for _, payload := range images {
		if _, err := buf.Write(payload); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func writePNG(path string, img image.Image) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		fatal(err)
	}
	writeFile(path, buf.Bytes())
}

func writeFile(path string, data []byte) {
	if err := os.WriteFile(path, data, 0644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %-40s %7d bytes\n", filepath.ToSlash(path), len(data))
}

func mustMkdir(p string) {
	if err := os.MkdirAll(p, 0755); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "genicon:", err)
	os.Exit(1)
}
