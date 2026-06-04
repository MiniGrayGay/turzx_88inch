package turing88

import (
	"image"
	"image/color"
	"testing"
)

func TestClipNativeImageCropsRightAndBottomOverflow(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 30))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})

	clipped, x, y, err := clipNativeImage(img, NativeWidth-10, NativeHeight-12)
	if err != nil {
		t.Fatal(err)
	}
	if x != NativeWidth-10 || y != NativeHeight-12 {
		t.Fatalf("origin = %d,%d, want %d,%d", x, y, NativeWidth-10, NativeHeight-12)
	}
	if gotW, gotH := imageSize(clipped); gotW != 10 || gotH != 12 {
		t.Fatalf("size = %dx%d, want 10x12", gotW, gotH)
	}
	if got := clipped.At(0, 0); got != img.At(0, 0) {
		t.Fatalf("top-left pixel changed: got %#v want %#v", got, img.At(0, 0))
	}
}

func TestClipNativeImageCropsLeftAndTopOverflow(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 30))
	img.Set(5, 7, color.RGBA{R: 9, G: 8, B: 7, A: 255})

	clipped, x, y, err := clipNativeImage(img, -5, -7)
	if err != nil {
		t.Fatal(err)
	}
	if x != 0 || y != 0 {
		t.Fatalf("origin = %d,%d, want 0,0", x, y)
	}
	if gotW, gotH := imageSize(clipped); gotW != 15 || gotH != 23 {
		t.Fatalf("size = %dx%d, want 15x23", gotW, gotH)
	}
	if got := clipped.At(0, 0); got != img.At(5, 7) {
		t.Fatalf("top-left pixel changed: got %#v want %#v", got, img.At(5, 7))
	}
}

func TestClipNativeImageRejectsFullyInvisibleRectangle(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 30))

	if _, _, _, err := clipNativeImage(img, NativeWidth, 0); err == nil {
		t.Fatal("expected error for rectangle outside right edge")
	}
	if _, _, _, err := clipNativeImage(img, 0, NativeHeight); err == nil {
		t.Fatal("expected error for rectangle outside bottom edge")
	}
	if _, _, _, err := clipNativeImage(img, -21, 0); err == nil {
		t.Fatal("expected error for rectangle outside left edge")
	}
}

func TestClipNativeBGRACropsRows(t *testing.T) {
	data := []byte{
		1, 2, 3, 4, 5, 6, 7, 8,
		9, 10, 11, 12, 13, 14, 15, 16,
	}

	clipped, width, height, x, y, err := clipNativeBGRA(data, 2, 2, -1, NativeHeight-1)
	if err != nil {
		t.Fatal(err)
	}
	if width != 1 || height != 1 || x != 0 || y != NativeHeight-1 {
		t.Fatalf("clip = %dx%d at %d,%d, want 1x1 at 0,%d", width, height, x, y, NativeHeight-1)
	}
	want := []byte{5, 6, 7, 8}
	for i := range want {
		if clipped[i] != want[i] {
			t.Fatalf("clipped[%d] = %d, want %d", i, clipped[i], want[i])
		}
	}
}
