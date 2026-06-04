package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"time"

	"turing-smart-screen-go/turing88"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	action := flag.String("action", "serve", "serve, list, init, show, update, clear, fps, off")
	port := flag.String("port", "AUTO", "serial port, for example COM4 or AUTO")
	listen := flag.String("listen", "127.0.0.1:60880", "HTTP/WebSocket listen address for the serve action")
	pipeName := flag.String("pipe", "ct88inch", "Windows named pipe name for JSON RPC; empty disables it")
	unixSocketPath := flag.String("unix-socket", "/tmp/ct88inch.sock", "Unix socket path for JSON RPC on non-Windows; empty disables it")
	imagePath := flag.String("image", "", "PNG/JPEG image path for show/update")
	x := flag.Int("x", 0, "update X coordinate")
	y := flag.Int("y", 0, "update Y coordinate")
	brightness := flag.Int("brightness", 40, "brightness percentage")
	orientationName := flag.String("orientation", "native", "native, landscape, reverse-landscape, portrait, reverse-portrait")
	reset := flag.Bool("reset", false, "reset display before initialization")
	clearColor := flag.String("color", "#000000", "clear color, #RRGGBB")
	targetFPS := flag.Float64("fps", 5, "target FPS for the fps action")
	seconds := flag.Int("seconds", 20, "duration in seconds for the fps action; 0 runs until interrupted")
	flag.Parse()

	switch strings.ToLower(*action) {
	case "list":
		return listPorts()
	case "serve":
		return runServer(*listen, *pipeName, *unixSocketPath, *port, *brightness, *orientationName, *reset)
	case "init", "show", "update", "clear", "fps", "off":
	default:
		return fmt.Errorf("unknown action %q", *action)
	}

	driver, err := turing88.Open(*port)
	if err != nil {
		return err
	}
	defer driver.Close()

	fmt.Printf("opened %s\n", driver.PortName())

	if strings.EqualFold(*action, "off") {
		return driver.ScreenOff()
	}

	if *reset {
		fmt.Println("resetting display")
		if err := driver.Reset(); err != nil {
			return err
		}
		fmt.Printf("reopened %s\n", driver.PortName())
	}

	id, err := driver.Initialize()
	if err != nil {
		return err
	}
	fmt.Printf("display id: %s, rom: %d\n", id, driver.ROMVersion())

	orientation, err := turing88.ParseOrientation(*orientationName)
	if err != nil {
		return err
	}
	if err := driver.ScreenOn(); err != nil {
		return err
	}
	if err := driver.SetBrightness(*brightness); err != nil {
		return err
	}
	if err := driver.SetOrientation(orientation); err != nil {
		return err
	}
	w, h := driver.DisplaySize()
	fmt.Printf("display size: %dx%d (%s), pixel format: bgra\n", w, h, orientation)

	switch strings.ToLower(*action) {
	case "init":
		return nil
	case "clear":
		c, err := parseHexColor(*clearColor)
		if err != nil {
			return err
		}
		return driver.Clear(c)
	case "fps":
		return runFPSTest(driver, *targetFPS, *seconds)
	case "show", "update":
		if *imagePath == "" {
			return fmt.Errorf("-image is required for %s", *action)
		}
		img, err := decodeImage(*imagePath)
		if err != nil {
			return err
		}
		if strings.EqualFold(*action, "show") {
			*x, *y = 0, 0
		}
		if err := driver.DisplayImage(img, *x, *y); err != nil {
			return err
		}
		fmt.Println("image sent")
		return nil
	}
	return nil
}

func runFPSTest(driver *turing88.Driver, targetFPS float64, seconds int) error {
	if targetFPS <= 0 {
		return fmt.Errorf("-fps must be > 0")
	}
	if seconds < 0 {
		return fmt.Errorf("-seconds must be >= 0")
	}

	w, h := driver.DisplaySize()
	frameInterval := time.Duration(float64(time.Second) / targetFPS)
	started := time.Now()
	var deadline time.Time
	if seconds > 0 {
		deadline = started.Add(time.Duration(seconds) * time.Second)
	}

	var sent int
	var totalSend time.Duration
	for {
		if !deadline.IsZero() && time.Now().After(deadline) {
			break
		}

		frameStart := time.Now()
		img := makeTestFrame(w, h, sent)
		if err := driver.DisplayImage(img, 0, 0); err != nil {
			return err
		}

		sendElapsed := time.Since(frameStart)
		totalSend += sendElapsed
		sent++

		fmt.Printf("frame=%d send=%s\n", sent, sendElapsed.Round(time.Millisecond))

		if sleep := frameInterval - time.Since(frameStart); sleep > 0 {
			time.Sleep(sleep)
		}
	}

	elapsed := time.Since(started)
	if sent == 0 {
		fmt.Println("no frames sent")
		return nil
	}
	fmt.Printf(
		"summary: frames=%d elapsed=%s actual_fps=%.2f avg_send=%s target_fps=%.2f\n",
		sent,
		elapsed.Round(time.Millisecond),
		float64(sent)/elapsed.Seconds(),
		time.Duration(int64(totalSend)/int64(sent)).Round(time.Millisecond),
		targetFPS,
	)
	return nil
}

func makeTestFrame(width, height, frame int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	base := color.RGBA{
		R: uint8(8 + (frame*7)%32),
		G: uint8(12 + (frame*5)%28),
		B: uint8(28 + (frame*11)%48),
		A: 255,
	}
	draw.Draw(img, img.Bounds(), &image.Uniform{C: base}, image.Point{}, draw.Src)

	stripeW := maxInt(24, width/8)
	x := (frame*maxInt(11, width/32))%(width+stripeW) - stripeW
	draw.Draw(
		img,
		image.Rect(x, 0, x+stripeW, height),
		&image.Uniform{C: color.RGBA{R: 255, G: 239, B: 47, A: 255}},
		image.Point{},
		draw.Src,
	)

	for i := 0; i < 8; i++ {
		y := (i*height)/8 + (frame*7)%maxInt(1, height/12)
		rect := image.Rect(0, y, width, minInt(y+maxInt(2, height/96), height))
		draw.Draw(
			img,
			rect,
			&image.Uniform{C: color.RGBA{R: 0, G: 225, B: 255, A: 255}},
			image.Point{},
			draw.Src,
		)
	}

	blockW := maxInt(20, width/24)
	blockH := maxInt(20, height/24)
	for i := 0; i < 12; i++ {
		bx := (frame*19 + i*width/12) % maxInt(1, width-blockW)
		by := (frame*23 + i*height/12) % maxInt(1, height-blockH)
		draw.Draw(
			img,
			image.Rect(bx, by, bx+blockW, by+blockH),
			&image.Uniform{C: color.RGBA{R: 255, G: 0, B: 120, A: 255}},
			image.Point{},
			draw.Src,
		)
	}

	return img
}

func minInt(a, b int) int {
	if a < b {
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

func listPorts() error {
	ports, err := turing88.ListPorts()
	if err != nil {
		return err
	}
	for _, port := range ports {
		fmt.Printf("%s\tUSB=%t\tVID=%s\tPID=%s\tSER=%s\t%s %s\n",
			port.Name,
			port.IsUSB,
			port.VID,
			port.PID,
			port.SerialNumber,
			port.Manufacturer,
			port.Product,
		)
	}
	return nil
}

func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return img, nil
}

func parseHexColor(value string) (color.RGBA, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(value) != 6 {
		return color.RGBA{}, fmt.Errorf("color must be #RRGGBB")
	}

	var r, g, b uint8
	if _, err := fmt.Sscanf(value, "%02x%02x%02x", &r, &g, &b); err != nil {
		return color.RGBA{}, err
	}
	return color.RGBA{R: r, G: g, B: b, A: 255}, nil
}

func init() {
	image.RegisterFormat("png", "png", png.Decode, png.DecodeConfig)
	image.RegisterFormat("jpeg", "jpeg", jpeg.Decode, jpeg.DecodeConfig)
	image.RegisterFormat("jpg", "jpg", jpeg.Decode, jpeg.DecodeConfig)
}
