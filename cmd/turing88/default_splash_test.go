package main

import (
	"testing"

	"turing-smart-screen-go/turing88"
)

func TestDefaultSplashNativeBGRA(t *testing.T) {
	data, err := defaultSplashNativeBGRA()
	if err != nil {
		t.Fatal(err)
	}

	want := turing88.NativeWidth * turing88.NativeHeight * 4
	if len(data) != want {
		t.Fatalf("default splash size = %d, want %d", len(data), want)
	}
}
