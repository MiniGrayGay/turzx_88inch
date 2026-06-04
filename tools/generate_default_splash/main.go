//go:build ignore

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	xdraw "golang.org/x/image/draw"
)

const (
	sourcePath = "../4_1.png"
	outputPath = "cmd/turing88/default_splash.go"

	landscapeWidth  = 1920
	landscapeHeight = 480
	nativeWidth     = 480
	nativeHeight    = 1920
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	src, err := loadPNG(sourcePath)
	if err != nil {
		return err
	}

	landscape := image.NewRGBA(image.Rect(0, 0, landscapeWidth, landscapeHeight))
	xdraw.CatmullRom.Scale(landscape, landscape.Bounds(), src, src.Bounds(), draw.Src, nil)

	nativeBGRA := rotateLandscapeToNativeBGRA(landscape)
	compressed, err := gzipBytes(nativeBGRA)
	if err != nil {
		return err
	}

	encoded := base64.StdEncoding.EncodeToString(compressed)
	code := renderSource(encoded, len(compressed), len(nativeBGRA))
	if err := os.WriteFile(outputPath, []byte(code), 0644); err != nil {
		return err
	}

	fmt.Printf("source: %s %dx%d\n", sourcePath, src.Bounds().Dx(), src.Bounds().Dy())
	fmt.Printf("scaled: %dx%d\n", landscapeWidth, landscapeHeight)
	fmt.Printf("native bgra: %dx%d %d bytes\n", nativeWidth, nativeHeight, len(nativeBGRA))
	fmt.Printf("compressed: %d bytes\n", len(compressed))
	fmt.Printf("written: %s\n", outputPath)
	return nil
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	return img, nil
}

func rotateLandscapeToNativeBGRA(img image.Image) []byte {
	out := make([]byte, 0, nativeWidth*nativeHeight*4)
	for y := 0; y < nativeHeight; y++ {
		for x := 0; x < nativeWidth; x++ {
			srcX := y
			srcY := landscapeHeight - 1 - x
			r, g, b, a := img.At(srcX, srcY).RGBA()
			out = append(out, byte(b>>8), byte(g>>8), byte(r>>8), byte(a>>8))
		}
	}
	return out
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func renderSource(encoded string, compressedSize, rawSize int) string {
	var chunks []string
	for len(encoded) > 0 {
		n := 120
		if len(encoded) < n {
			n = len(encoded)
		}
		chunks = append(chunks, encoded[:n])
		encoded = encoded[n:]
	}

	var b strings.Builder
	b.WriteString("package main\n\n")
	b.WriteString("import (\n")
	b.WriteString("\t\"bytes\"\n")
	b.WriteString("\t\"compress/gzip\"\n")
	b.WriteString("\t\"encoding/base64\"\n")
	b.WriteString("\t\"fmt\"\n")
	b.WriteString("\t\"io\"\n")
	b.WriteString("\t\"strings\"\n")
	b.WriteString("\t\"sync\"\n\n")
	b.WriteString("\t\"turing-smart-screen-go/turing88\"\n")
	b.WriteString(")\n\n")
	b.WriteString("// Generated from " + filepath.ToSlash(sourcePath) + ". Do not edit manually.\n")
	b.WriteString("const (\n")
	fmt.Fprintf(&b, "\tdefaultSplashLandscapeWidth = %d\n", landscapeWidth)
	fmt.Fprintf(&b, "\tdefaultSplashLandscapeHeight = %d\n", landscapeHeight)
	b.WriteString("\tdefaultSplashNativeWidth = turing88.NativeWidth\n")
	b.WriteString("\tdefaultSplashNativeHeight = turing88.NativeHeight\n")
	fmt.Fprintf(&b, "\tdefaultSplashRawSize = %d\n", rawSize)
	fmt.Fprintf(&b, "\tdefaultSplashCompressedSize = %d\n", compressedSize)
	b.WriteString(")\n\n")
	b.WriteString("var (\n")
	b.WriteString("\tdefaultSplashOnce sync.Once\n")
	b.WriteString("\tdefaultSplashData []byte\n")
	b.WriteString("\tdefaultSplashErr error\n")
	b.WriteString(")\n\n")
	b.WriteString("func defaultSplashNativeBGRA() ([]byte, error) {\n")
	b.WriteString("\tdefaultSplashOnce.Do(func() {\n")
	b.WriteString("\t\tcompressed, err := base64.StdEncoding.DecodeString(strings.Join(defaultSplashBGRACompressedBase64, \"\"))\n")
	b.WriteString("\t\tif err != nil {\n")
	b.WriteString("\t\t\tdefaultSplashErr = err\n")
	b.WriteString("\t\t\treturn\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t\tif len(compressed) != defaultSplashCompressedSize {\n")
	b.WriteString("\t\t\tdefaultSplashErr = fmt.Errorf(\"default splash compressed size mismatch: got %d, want %d\", len(compressed), defaultSplashCompressedSize)\n")
	b.WriteString("\t\t\treturn\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t\tzr, err := gzip.NewReader(bytes.NewReader(compressed))\n")
	b.WriteString("\t\tif err != nil {\n")
	b.WriteString("\t\t\tdefaultSplashErr = err\n")
	b.WriteString("\t\t\treturn\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t\tdefer zr.Close()\n")
	b.WriteString("\t\tdata, err := io.ReadAll(zr)\n")
	b.WriteString("\t\tif err != nil {\n")
	b.WriteString("\t\t\tdefaultSplashErr = err\n")
	b.WriteString("\t\t\treturn\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t\tif len(data) != defaultSplashRawSize {\n")
	b.WriteString("\t\t\tdefaultSplashErr = fmt.Errorf(\"default splash raw size mismatch: got %d, want %d\", len(data), defaultSplashRawSize)\n")
	b.WriteString("\t\t\treturn\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t\tdefaultSplashData = data\n")
	b.WriteString("\t})\n")
	b.WriteString("\treturn defaultSplashData, defaultSplashErr\n")
	b.WriteString("}\n\n")
	b.WriteString("var defaultSplashBGRACompressedBase64 = []string{\n")
	for _, chunk := range chunks {
		fmt.Fprintf(&b, "\t%q,\n", chunk)
	}
	b.WriteString("}\n")
	return b.String()
}
