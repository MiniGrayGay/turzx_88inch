package turing88

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
)

const (
	NativeWidth  = 480
	NativeHeight = 1920

	baseWidth  = NativeWidth
	baseHeight = NativeHeight
	baudRate   = 115200
)

var (
	cmdHello              = []byte{0x01, 0xef, 0x69, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0xc5, 0xd3}
	cmdOptions            = []byte{0x7d, 0xef, 0x69, 0x00, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00, 0x2d}
	cmdRestart            = []byte{0x84, 0xef, 0x69, 0x00, 0x00, 0x00, 0x01}
	cmdTurnOff            = []byte{0x83, 0xef, 0x69, 0x00, 0x00, 0x00, 0x01}
	cmdSetBrightness      = []byte{0x7b, 0xef, 0x69, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00}
	cmdStopVideo          = []byte{0x79, 0xef, 0x69, 0x00, 0x00, 0x00, 0x01}
	cmdStopMedia          = []byte{0x96, 0xef, 0x69, 0x00, 0x00, 0x00, 0x01}
	cmdQueryStatus        = []byte{0xcf, 0xef, 0x69, 0x00, 0x00, 0x00, 0x01}
	cmdStartDisplayBitmap = []byte{0x2c}
	cmdPreUpdateBitmap    = []byte{0x86, 0xef, 0x69, 0x00, 0x00, 0x00, 0x01}
	cmdUpdateBitmap       = []byte{0xcc, 0xef, 0x69, 0x00}
	cmdDisplayBitmap88    = []byte{0xc8, 0xef, 0x69, 0x00, 0x38, 0x40}
)

type Orientation int

const (
	// The protocol-native service path uses ReversePortrait for "native" / no rotation.
	// Portrait is kept for the legacy DisplayImage rotation path and means 180 degrees
	// relative to the native 480x1920 coordinate system.
	Portrait Orientation = iota
	ReversePortrait
	Landscape
	ReverseLandscape
)

func ParseOrientation(value string) (Orientation, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "native", "none", "no-rotation", "no_rotation", "norotation", "0":
		return ReversePortrait, nil
	case "portrait":
		return Portrait, nil
	case "reverse-portrait", "reverse_portrait":
		return ReversePortrait, nil
	case "landscape":
		return Landscape, nil
	case "reverse-landscape", "reverse_landscape":
		return ReverseLandscape, nil
	default:
		return Landscape, fmt.Errorf("unknown orientation %q", value)
	}
}

func (o Orientation) String() string {
	switch o {
	case Portrait:
		return "portrait"
	case ReversePortrait:
		return "native"
	case Landscape:
		return "landscape"
	case ReverseLandscape:
		return "reverse-landscape"
	default:
		return "unknown"
	}
}

type PortInfo struct {
	Name         string
	VID          string
	PID          string
	SerialNumber string
	Product      string
	Manufacturer string
	IsUSB        bool
}

type Driver struct {
	port        serial.Port
	portName    string
	orientation Orientation
	romVersion  int
	updateCount uint32
}

func ListPorts() ([]PortInfo, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}

	out := make([]PortInfo, 0, len(ports))
	for _, p := range ports {
		out = append(out, PortInfo{
			Name:         p.Name,
			VID:          strings.ToUpper(p.VID),
			PID:          strings.ToUpper(p.PID),
			SerialNumber: p.SerialNumber,
			Product:      p.Product,
			Manufacturer: p.Manufacturer,
			IsUSB:        p.IsUSB,
		})
	}
	return out, nil
}

func AutoDetectPort() (string, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return "", err
	}

	if name := findAwakePort(ports); name != "" {
		return name, nil
	}

	for _, p := range ports {
		if isSleepingPort(p) {
			wakeSleepingPort(p.Name)
			break
		}
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		ports, err = enumerator.GetDetailedPortsList()
		if err != nil {
			return "", err
		}
		if name := findAwakePort(ports); name != "" {
			return name, nil
		}
	}

	return "", errors.New("no awake Turing 8.8-inch serial port found")
}

func Open(portName string) (*Driver, error) {
	if strings.EqualFold(portName, "AUTO") || strings.TrimSpace(portName) == "" {
		name, err := AutoDetectPort()
		if err != nil {
			return nil, err
		}
		portName = name
	}

	port, err := serial.Open(portName, serialMode())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", portName, err)
	}
	if err := port.SetReadTimeout(time.Second); err != nil {
		_ = port.Close()
		return nil, err
	}

	return &Driver{
		port:        port,
		portName:    portName,
		orientation: ReversePortrait,
		romVersion:  87,
	}, nil
}

func (d *Driver) PortName() string {
	return d.portName
}

func (d *Driver) ROMVersion() int {
	return d.romVersion
}

func (d *Driver) Close() error {
	if d.port == nil {
		return nil
	}
	err := d.port.Close()
	d.port = nil
	return err
}

func (d *Driver) Initialize() (string, error) {
	var response string
	for attempts := 0; attempts < 10; attempts++ {
		if err := d.port.ResetInputBuffer(); err != nil {
			return "", err
		}
		if err := d.sendCommand(cmdHello, nil, 0x00, 0); err != nil {
			return "", err
		}

		raw, err := d.readAtMost(23, time.Second)
		if err != nil {
			return "", err
		}
		response = printableASCII(raw)
		if strings.HasPrefix(response, "chs_") {
			break
		}
		time.Sleep(time.Second)
	}

	if !strings.HasPrefix(response, "chs_") {
		return response, fmt.Errorf("display returned invalid ID %q", response)
	}
	if !strings.HasPrefix(response, "chs_88inch") {
		return response, fmt.Errorf("display is not the supported 8.8-inch model: %q", response)
	}

	d.romVersion = parseROMVersion(response)
	return response, nil
}

func (d *Driver) Reset() error {
	if err := d.sendCommand(cmdRestart, nil, 0x00, 0); err != nil {
		return err
	}
	oldPort := d.portName
	if err := d.Close(); err != nil {
		return err
	}

	waitForAwakePort(false, 15*time.Second)
	name, err := waitForAwakePort(true, 15*time.Second)
	if err != nil {
		name = oldPort
	}

	reopened, err := Open(name)
	if err != nil {
		return err
	}
	*d = *reopened
	return nil
}

func (d *Driver) ScreenOn() error {
	if err := d.sendCommand(cmdStopVideo, nil, 0x00, 0); err != nil {
		return err
	}
	return d.sendCommand(cmdStopMedia, nil, 0x00, 1024)
}

func (d *Driver) ScreenOff() error {
	if err := d.sendCommand(cmdStopVideo, nil, 0x00, 0); err != nil {
		return err
	}
	if err := d.sendCommand(cmdStopMedia, nil, 0x00, 1024); err != nil {
		return err
	}
	return d.sendCommand(cmdTurnOff, nil, 0x00, 0)
}

func (d *Driver) SetBrightness(level int) error {
	if level < 0 || level > 100 {
		return fmt.Errorf("brightness must be in [0,100], got %d", level)
	}
	converted := byte(math.Round(float64(level) / 100 * 255))
	return d.sendCommand(cmdSetBrightness, []byte{converted}, 0x00, 0)
}

func (d *Driver) SetOrientation(orientation Orientation) error {
	d.orientation = orientation
	payload := []byte{0x00, 0x00, 0x00, 0x00}
	return d.sendCommand(cmdOptions, payload, 0x00, 0)
}

func (d *Driver) DisplayImage(img image.Image, x, y int) error {
	if img == nil {
		return errors.New("image is nil")
	}
	screenW, screenH := d.DisplaySize()
	if x < 0 || y < 0 || x >= screenW || y >= screenH {
		return fmt.Errorf("image origin %d,%d is outside display %dx%d", x, y, screenW, screenH)
	}

	img = cropToFit(img, screenW-x, screenH-y)
	w, h := imageSize(img)
	if w <= 0 || h <= 0 {
		return errors.New("image has empty dimensions")
	}

	if x == 0 && y == 0 && w == screenW && h == screenH {
		return d.displayFullImage(img)
	}
	return d.displayUpdateImage(img, x, y)
}

func (d *Driver) DisplayNativeBGRA(data []byte, width, height, x, y int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid image size %dx%d", width, height)
	}
	if len(data) != width*height*4 {
		return fmt.Errorf("BGRA payload length must be %d bytes, got %d", width*height*4, len(data))
	}
	clipped, clippedWidth, clippedHeight, clippedX, clippedY, err := clipNativeBGRA(data, width, height, x, y)
	if err != nil {
		return err
	}

	if clippedX == 0 && clippedY == 0 && clippedWidth == baseWidth && clippedHeight == baseHeight {
		return d.displayFullNativeBGRA(clipped)
	}
	return d.displayUpdateNativeBGRA(clipped, clippedWidth, clippedHeight, clippedX, clippedY)
}

func (d *Driver) DisplayNativeImage(img image.Image, x, y int) error {
	if img == nil {
		return errors.New("image is nil")
	}
	var err error
	img, x, y, err = clipNativeImage(img, x, y)
	if err != nil {
		return err
	}
	width, height := imageSize(img)
	if width <= 0 || height <= 0 {
		return errors.New("image has empty dimensions after clipping")
	}
	return d.DisplayNativeBGRA(imageToBGRA(img), width, height, x, y)
}

func (d *Driver) Clear(c color.Color) error {
	screenW, screenH := d.DisplaySize()
	img := image.NewRGBA(image.Rect(0, 0, screenW, screenH))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return d.DisplayImage(img, 0, 0)
}

func (d *Driver) DisplaySize() (int, int) {
	switch d.orientation {
	case Portrait, ReversePortrait:
		return baseWidth, baseHeight
	default:
		return baseHeight, baseWidth
	}
}

func (d *Driver) displayFullImage(img image.Image) error {
	if err := d.sendCommand(cmdPreUpdateBitmap, nil, 0x00, 0); err != nil {
		return err
	}
	if err := d.sendCommand(cmdStartDisplayBitmap, nil, 0x2c, 0); err != nil {
		return err
	}

	displayPayload := make([]byte, 2)
	binary.BigEndian.PutUint16(displayPayload, uint16(baseWidth*baseWidth/64))
	if err := d.sendCommand(cmdDisplayBitmap88, displayPayload, 0x00, 0); err != nil {
		return err
	}

	payload := generateFullImagePayload(img, d.orientation)
	if err := d.sendCommand(nil, payload, 0x00, 1024); err != nil {
		return err
	}
	return d.sendCommand(cmdQueryStatus, nil, 0x00, 1024)
}

func (d *Driver) displayFullNativeBGRA(data []byte) error {
	if err := d.sendCommand(cmdPreUpdateBitmap, nil, 0x00, 0); err != nil {
		return err
	}
	if err := d.sendCommand(cmdStartDisplayBitmap, nil, 0x2c, 0); err != nil {
		return err
	}

	displayPayload := make([]byte, 2)
	binary.BigEndian.PutUint16(displayPayload, uint16(baseWidth*baseWidth/64))
	if err := d.sendCommand(cmdDisplayBitmap88, displayPayload, 0x00, 0); err != nil {
		return err
	}

	if err := d.sendCommand(nil, joinChunks(data, 249, 0x00), 0x00, 1024); err != nil {
		return err
	}
	return d.sendCommand(cmdQueryStatus, nil, 0x00, 1024)
}

func (d *Driver) displayUpdateImage(img image.Image, x, y int) error {
	imgPayload, headerPayload := d.generateUpdateImagePayload(img, x, y)
	if err := d.sendCommand(nil, headerPayload, 0x00, 0); err != nil {
		return err
	}
	if err := d.sendCommand(nil, imgPayload, 0x00, 0); err != nil {
		return err
	}
	return d.sendCommand(cmdQueryStatus, nil, 0x00, 1024)
}

func (d *Driver) displayUpdateNativeBGRA(data []byte, width, height, x, y int) error {
	imgPayload, headerPayload := d.generateNativeUpdateBGRAPayload(data, width, height, x, y)
	if err := d.sendCommand(nil, headerPayload, 0x00, 0); err != nil {
		return err
	}
	if err := d.sendCommand(nil, imgPayload, 0x00, 0); err != nil {
		return err
	}
	return d.sendCommand(cmdQueryStatus, nil, 0x00, 1024)
}

func (d *Driver) generateNativeUpdateBGRAPayload(data []byte, width, height, x, y int) ([]byte, []byte) {
	raw := bytes.NewBuffer(make([]byte, 0, height*(5+width*4)))
	for row := 0; row < height; row++ {
		address := uint32((y+row)*baseWidth + x)
		raw.Write(threeBytes(address))
		raw.Write(twoBytes(uint16(width)))
		start := row * width * 4
		raw.Write(data[start : start+width*4])
	}

	imageSizeBytes := threeBytes(uint32(raw.Len() + 2))
	header := bytes.NewBuffer(make([]byte, 0, 14))
	header.Write(cmdUpdateBitmap)
	header.Write(imageSizeBytes)
	header.Write([]byte{0x00, 0x00, 0x00})
	header.Write(fourBytes(d.updateCount))
	d.updateCount++

	imgPayload := raw.Bytes()
	if len(imgPayload) > 250 {
		imgPayload = joinChunks(imgPayload, 249, 0x00)
	}
	imgPayload = append(imgPayload, 0xef, 0x69)
	return imgPayload, header.Bytes()
}

func (d *Driver) generateUpdateImagePayload(img image.Image, x, y int) ([]byte, []byte) {
	x0, y0 := x, y
	switch d.orientation {
	case Landscape:
		img = rotate90CW(img)
		w, _ := imageSize(img)
		_, screenH := d.DisplaySize()
		y0 = screenH - y - w
	case ReverseLandscape:
		img = rotate90CCW(img)
		_, h := imageSize(img)
		screenW, _ := d.DisplaySize()
		x0 = screenW - x - h
	case Portrait:
		img = rotate180(img)
		w, h := imageSize(img)
		_, screenH := d.DisplaySize()
		x0 = screenH - y - h
		y0 = screenH - x - w
	case ReversePortrait:
		x0, y0 = y, x
	}

	imgData, pixelSize := imageToBGRA(img), 4

	w, h := imageSize(img)
	raw := bytes.NewBuffer(make([]byte, 0, h*(5+w*pixelSize)))
	for row := 0; row < h; row++ {
		address := uint32((x0+row)*baseWidth + y0)
		raw.Write(threeBytes(address))
		raw.Write(twoBytes(uint16(w)))
		start := row * w * pixelSize
		raw.Write(imgData[start : start+w*pixelSize])
	}

	imageSizeBytes := threeBytes(uint32(raw.Len() + 2))
	header := bytes.NewBuffer(make([]byte, 0, 14))
	header.Write(cmdUpdateBitmap)
	header.Write(imageSizeBytes)
	header.Write([]byte{0x00, 0x00, 0x00})
	header.Write(fourBytes(d.updateCount))
	d.updateCount++

	imgPayload := raw.Bytes()
	if len(imgPayload) > 250 {
		imgPayload = joinChunks(imgPayload, 249, 0x00)
	}
	imgPayload = append(imgPayload, 0xef, 0x69)
	return imgPayload, header.Bytes()
}

func (d *Driver) sendCommand(command, payload []byte, padding byte, readSize int) error {
	message := make([]byte, 0, len(command)+len(payload)+250)
	if command != nil {
		message = append(message, command...)
	}
	message = append(message, payload...)

	if rem := len(message) % 250; rem != 0 {
		message = append(message, bytes.Repeat([]byte{padding}, 250-rem)...)
	}

	if err := d.writeAll(message); err != nil {
		return err
	}
	if readSize > 0 {
		_, err := d.readAtMost(readSize, time.Second)
		return err
	}
	return nil
}

func (d *Driver) writeAll(data []byte) error {
	for len(data) > 0 {
		n, err := d.port.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return d.port.Drain()
}

func (d *Driver) readAtMost(size int, idleTimeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(idleTimeout)
	out := make([]byte, 0, size)
	buf := make([]byte, size)

	for len(out) < size && time.Now().Before(deadline) {
		n, err := d.port.Read(buf[:size-len(out)])
		if n > 0 {
			out = append(out, buf[:n]...)
			deadline = time.Now().Add(idleTimeout)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return out, err
		}
		if n == 0 {
			break
		}
	}
	return out, nil
}

func serialMode() *serial.Mode {
	return &serial.Mode{
		BaudRate: baudRate,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
		InitialStatusBits: &serial.ModemOutputBits{
			RTS: true,
			DTR: true,
		},
	}
}

func findAwakePort(ports []*enumerator.PortDetails) string {
	for _, p := range ports {
		if equalVIDPID(p, "0525", "A4A7") ||
			equalVIDPID(p, "1D6B", "0121") ||
			equalVIDPID(p, "1D6B", "0106") ||
			p.SerialNumber == "20080411" {
			return p.Name
		}
	}
	return ""
}

func isSleepingPort(p *enumerator.PortDetails) bool {
	return p.SerialNumber == "USB7INCH" ||
		p.SerialNumber == "CT21INCH" ||
		p.SerialNumber == "CT88INCH" ||
		equalVIDPID(p, "1A86", "CA21") ||
		equalVIDPID(p, "1A86", "CA88")
}

func wakeSleepingPort(name string) {
	for i := 0; i < 15; i++ {
		port, err := serial.Open(name, serialMode())
		if err == nil {
			_ = port.SetReadTimeout(time.Second)
			_ = port.Close()
			return
		}
		time.Sleep(time.Second)
	}
}

func waitForAwakePort(wantPresent bool, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ports, err := enumerator.GetDetailedPortsList()
		if err != nil {
			return "", err
		}
		name := findAwakePort(ports)
		if wantPresent && name != "" {
			return name, nil
		}
		if !wantPresent && name == "" {
			return "", nil
		}
		time.Sleep(time.Second)
	}
	if wantPresent {
		return "", errors.New("awake port did not appear")
	}
	return "", errors.New("awake port did not disappear")
}

func equalVIDPID(p *enumerator.PortDetails, vid, pid string) bool {
	return strings.EqualFold(p.VID, vid) && strings.EqualFold(p.PID, pid)
}

func printableASCII(data []byte) string {
	var b strings.Builder
	for _, c := range data {
		if c >= 0x20 && c <= 0x7e {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func parseROMVersion(response string) int {
	parts := strings.Split(response, ".")
	if len(parts) < 3 {
		return 87
	}
	version, err := strconv.Atoi(parts[2])
	if err != nil || version < 80 || version > 100 {
		return 87
	}
	return version
}

func generateFullImagePayload(img image.Image, orientation Orientation) []byte {
	switch orientation {
	case Landscape:
		img = rotate90CW(img)
	case ReverseLandscape:
		img = rotate90CCW(img)
	case Portrait:
		img = rotate180(img)
	case ReversePortrait:
	}
	return joinChunks(imageToBGRA(img), 249, 0x00)
}

func cropToFit(img image.Image, maxW, maxH int) image.Image {
	if maxW <= 0 || maxH <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 0, 0))
	}
	w, h := imageSize(img)
	if w <= maxW && h <= maxH {
		return normalizeImage(img)
	}
	if maxW < w {
		w = maxW
	}
	if maxH < h {
		h = maxH
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Src)
	return dst
}

func clipNativeImage(img image.Image, x, y int) (image.Image, int, int, error) {
	img = normalizeImage(img)
	w, h := imageSize(img)
	srcX, srcY := 0, 0

	if x < 0 {
		srcX = -x
		x = 0
	}
	if y < 0 {
		srcY = -y
		y = 0
	}
	if x >= baseWidth || y >= baseHeight || srcX >= w || srcY >= h {
		return nil, x, y, fmt.Errorf("image rectangle is outside native display %dx%d", baseWidth, baseHeight)
	}

	clippedW := minInt(w-srcX, baseWidth-x)
	clippedH := minInt(h-srcY, baseHeight-y)
	if clippedW <= 0 || clippedH <= 0 {
		return nil, x, y, errors.New("image has empty dimensions after clipping")
	}
	if srcX == 0 && srcY == 0 && clippedW == w && clippedH == h {
		return img, x, y, nil
	}

	dst := image.NewRGBA(image.Rect(0, 0, clippedW, clippedH))
	draw.Draw(dst, dst.Bounds(), img, image.Point{X: srcX, Y: srcY}, draw.Src)
	return dst, x, y, nil
}

func clipNativeBGRA(data []byte, width, height, x, y int) ([]byte, int, int, int, int, error) {
	srcX, srcY := 0, 0
	if x < 0 {
		srcX = -x
		x = 0
	}
	if y < 0 {
		srcY = -y
		y = 0
	}
	if x >= baseWidth || y >= baseHeight || srcX >= width || srcY >= height {
		return nil, 0, 0, x, y, fmt.Errorf("image rectangle is outside native display %dx%d", baseWidth, baseHeight)
	}

	clippedWidth := minInt(width-srcX, baseWidth-x)
	clippedHeight := minInt(height-srcY, baseHeight-y)
	if clippedWidth <= 0 || clippedHeight <= 0 {
		return nil, 0, 0, x, y, errors.New("image has empty dimensions after clipping")
	}
	if srcX == 0 && srcY == 0 && clippedWidth == width && clippedHeight == height {
		return data, width, height, x, y, nil
	}

	out := make([]byte, clippedWidth*clippedHeight*4)
	for row := 0; row < clippedHeight; row++ {
		srcStart := ((srcY+row)*width + srcX) * 4
		dstStart := row * clippedWidth * 4
		copy(out[dstStart:dstStart+clippedWidth*4], data[srcStart:srcStart+clippedWidth*4])
	}
	return out, clippedWidth, clippedHeight, x, y, nil
}

func normalizeImage(img image.Image) image.Image {
	b := img.Bounds()
	if b.Min.X == 0 && b.Min.Y == 0 {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

func imageSize(img image.Image) (int, int) {
	b := img.Bounds()
	return b.Dx(), b.Dy()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func imageToBGRA(img image.Image) []byte {
	img = normalizeImage(img)
	w, h := imageSize(img)
	out := make([]byte, 0, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			out = append(out, byte(b>>8), byte(g>>8), byte(r>>8), byte(a>>8))
		}
	}
	return out
}

func rotate90CW(img image.Image) image.Image {
	img = normalizeImage(img)
	w, h := imageSize(img)
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(h-1-y, x, img.At(x, y))
		}
	}
	return dst
}

func rotate90CCW(img image.Image) image.Image {
	img = normalizeImage(img)
	w, h := imageSize(img)
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(y, w-1-x, img.At(x, y))
		}
	}
	return dst
}

func rotate180(img image.Image) image.Image {
	img = normalizeImage(img)
	w, h := imageSize(img)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, h-1-y, img.At(x, y))
		}
	}
	return dst
}

func joinChunks(data []byte, chunkSize int, sep byte) []byte {
	if len(data) <= chunkSize {
		return append([]byte(nil), data...)
	}
	out := make([]byte, 0, len(data)+len(data)/chunkSize)
	for i := 0; i < len(data); i += chunkSize {
		if i > 0 {
			out = append(out, sep)
		}
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		out = append(out, data[i:end]...)
	}
	return out
}

func threeBytes(value uint32) []byte {
	return []byte{byte(value >> 16), byte(value >> 8), byte(value)}
}

func twoBytes(value uint16) []byte {
	return []byte{byte(value >> 8), byte(value)}
}

func fourBytes(value uint32) []byte {
	return []byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)}
}
