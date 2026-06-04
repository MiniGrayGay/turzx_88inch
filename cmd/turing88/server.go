package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"

	"turing-smart-screen-go/turing88"
)

const maxImageUploadBytes = 64 << 20

type rpcValue struct {
	present bool
	text    string
}

func (v *rpcValue) UnmarshalJSON(data []byte) error {
	v.present = true

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		v.text = ""
		return nil
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		v.text = s
		return nil
	}

	v.text = trimmed
	return nil
}

func (v rpcValue) String() string {
	return strings.TrimSpace(v.text)
}

func (v rpcValue) IsSet() bool {
	return v.present && v.String() != ""
}

type rpcRequest struct {
	Action      rpcValue `json:"action"`
	Port        rpcValue `json:"port"`
	Brightness  rpcValue `json:"brightness"`
	Orientation rpcValue `json:"orientation"`
	Image       rpcValue `json:"image"`
	PositionX   rpcValue `json:"position_X"`
	PositionY   rpcValue `json:"position_Y"`
	Color       rpcValue `json:"color"`
	Reset       rpcValue `json:"reset"`
}

type rpcResponse struct {
	OK      bool                `json:"ok"`
	Action  string              `json:"action,omitempty"`
	Message string              `json:"message,omitempty"`
	Status  *serviceStatus      `json:"status,omitempty"`
	Ports   []turing88.PortInfo `json:"ports,omitempty"`
	Error   string              `json:"error,omitempty"`
}

type serviceStatus struct {
	Initialized    bool   `json:"initialized"`
	ConfiguredPort string `json:"configured_port"`
	Port           string `json:"port"`
	DisplayID      string `json:"display_id"`
	ROMVersion     int    `json:"rom_version"`
	NativeWidth    int    `json:"native_width"`
	NativeHeight   int    `json:"native_height"`
	PixelFormat    string `json:"pixel_format"`
	Brightness     int    `json:"brightness"`
	Orientation    string `json:"orientation"`
	StartedAt      string `json:"started_at"`
	UpdatedAt      string `json:"updated_at"`
}

type screenService struct {
	mu             sync.Mutex
	driver         *turing88.Driver
	configuredPort string
	displayID      string
	brightness     int
	orientation    turing88.Orientation
	initialized    bool
	startedAt      time.Time
	updatedAt      time.Time
}

func runServer(listen, pipeName, unixSocketPath, port string, brightness int, orientationName string, reset bool) error {
	orientation, err := turing88.ParseOrientation(orientationName)
	if err != nil {
		return err
	}
	if brightness < 0 || brightness > 100 {
		return fmt.Errorf("brightness must be in [0,100], got %d", brightness)
	}

	service := &screenService{
		configuredPort: strings.TrimSpace(port),
		brightness:     brightness,
		orientation:    orientation,
		startedAt:      time.Now(),
		updatedAt:      time.Now(),
	}
	if service.configuredPort == "" {
		service.configuredPort = "AUTO"
	}
	if err := service.initialize(reset); err != nil {
		return err
	}
	localRPCEndpoint, err := startLocalRPC(service, pipeName, unixSocketPath)
	if err != nil {
		return err
	}
	httpListener, httpEndpoint, err := listenTCPAutoDecrement(listen)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"name":          "turing88 driver",
			"local_rpc":     localRPCEndpoint,
			"http_rpc":      "POST /rpc",
			"http_image":    "POST /image",
			"websocket_rpc": "GET /rpc",
			"status":        "GET /status",
			"ports":         "GET /ports",
		})
	})
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			handleWebSocket(service, w, r)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, rpcResponse{OK: false, Error: "use POST /rpc or WebSocket /rpc"})
			return
		}
		handleRPC(service, w, r)
	})
	mux.HandleFunc("/image", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, rpcResponse{OK: false, Error: "use POST /image"})
			return
		}
		handleImageUpload(service, w, r)
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, rpcResponse{OK: true, Action: "status", Status: service.status()})
	})
	mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		ports, err := turing88.ListPorts()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, rpcResponse{OK: false, Action: "ports", Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{OK: true, Action: "ports", Ports: ports, Status: service.status()})
	})

	fmt.Printf("driver ready: port=%s id=%s rom=%d native=%dx%d format=BGRA\n",
		service.status().Port,
		service.status().DisplayID,
		service.status().ROMVersion,
		turing88.NativeWidth,
		turing88.NativeHeight,
	)
	if localRPCEndpoint != "" {
		fmt.Printf("listening on local rpc %s\n", localRPCEndpoint)
	}
	fmt.Printf("listening on http://%s\n", httpEndpoint)
	return http.Serve(httpListener, mux)
}

func handleRPC(service *screenService, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{OK: false, Error: err.Error()})
		return
	}

	resp := service.execute(req)
	status := http.StatusOK
	if !resp.OK {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, resp)
}

func handleImageUpload(service *screenService, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	x, err := parseImageQueryInt(r, 0, "x", "position_X", "position_x")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{OK: false, Action: "image", Error: err.Error(), Status: service.status()})
		return
	}
	y, err := parseImageQueryInt(r, 0, "y", "position_Y", "position_y")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{OK: false, Action: "image", Error: err.Error(), Status: service.status()})
		return
	}

	img, err := readUploadedImage(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{OK: false, Action: "image", Error: err.Error(), Status: service.status()})
		return
	}
	if err := service.displayNativeImage(img, x, y); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{OK: false, Action: "image", Error: err.Error(), Status: service.status()})
		return
	}
	writeJSON(w, http.StatusOK, rpcResponse{OK: true, Action: "image", Message: "image sent", Status: service.status()})
}

func handleWebSocket(service *screenService, w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			_ = conn.WriteJSON(rpcResponse{OK: false, Error: "send JSON text messages"})
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal(data, &req); err != nil {
			_ = conn.WriteJSON(rpcResponse{OK: false, Error: err.Error()})
			continue
		}
		if err := conn.WriteJSON(service.execute(req)); err != nil {
			return
		}
	}
}

func handleJSONRPCConn(service *screenService, conn io.ReadWriteCloser) {
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	for {
		var req rpcRequest
		if err := decoder.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			_ = encoder.Encode(rpcResponse{OK: false, Error: err.Error()})
			return
		}

		if err := encoder.Encode(service.execute(req)); err != nil {
			return
		}
	}
}

func (s *screenService) execute(req rpcRequest) rpcResponse {
	action := strings.ToLower(req.Action.String())
	if action == "" {
		action = "status"
	}

	switch action {
	case "status":
		return rpcResponse{OK: true, Action: action, Status: s.status()}
	case "ports", "list":
		ports, err := turing88.ListPorts()
		if err != nil {
			return s.errorResponse(action, err)
		}
		return rpcResponse{OK: true, Action: "ports", Ports: ports, Status: s.status()}
	case "init", "initialize", "reinit", "reconnect":
		requestedPort := ""
		if req.Port.IsSet() {
			requestedPort = normalizePort(req.Port.String())
		}
		reset := parseBool(req.Reset)
		needsInitialize := s.needsInitialize(requestedPort, reset)

		if needsInitialize {
			if requestedPort != "" {
				s.setConfiguredPort(requestedPort)
			}
			if err := s.applyRequestedSettingsState(req); err != nil {
				return s.errorResponse(action, err)
			}
			if err := s.initialize(reset); err != nil {
				return s.errorResponse(action, err)
			}
			return rpcResponse{OK: true, Action: "init", Message: "initialized", Status: s.status()}
		}

		if requestedPort != "" {
			s.setConfiguredPort(requestedPort)
		}
		if err := s.applyRequestedSettings(req); err != nil {
			return s.errorResponse(action, err)
		}
		return rpcResponse{OK: true, Action: "init", Message: "already initialized", Status: s.status()}
	case "on", "screen_on":
		if err := s.withDriver(func(d *turing88.Driver) error {
			return d.ScreenOn()
		}); err != nil {
			return s.errorResponse(action, err)
		}
		return rpcResponse{OK: true, Action: "on", Message: "screen on", Status: s.status()}
	case "off", "screen_off":
		if err := s.withDriver(func(d *turing88.Driver) error {
			return d.ScreenOff()
		}); err != nil {
			return s.errorResponse(action, err)
		}
		return rpcResponse{OK: true, Action: "off", Message: "screen off", Status: s.status()}
	case "brightness", "set_brightness":
		if err := s.applyBrightness(req.Brightness); err != nil {
			return s.errorResponse(action, err)
		}
		return rpcResponse{OK: true, Action: "brightness", Message: "brightness updated", Status: s.status()}
	case "clear":
		if err := s.applyRequestedSettings(req); err != nil {
			return s.errorResponse(action, err)
		}
		c, err := parseRPCColor(req.Color)
		if err != nil {
			return s.errorResponse(action, err)
		}
		if err := s.clearNative(c); err != nil {
			return s.errorResponse(action, err)
		}
		return rpcResponse{OK: true, Action: "clear", Message: "screen cleared", Status: s.status()}
	case "show", "full":
		if err := s.applyRequestedSettings(req); err != nil {
			return s.errorResponse(action, err)
		}
		img, err := loadRPCImage(req.Image.String())
		if err != nil {
			return s.errorResponse(action, err)
		}
		width, height := imageSizeOf(img)
		if width != turing88.NativeWidth || height != turing88.NativeHeight {
			return s.errorResponse(action, fmt.Errorf("show requires a native full-screen %dx%d image, got %dx%d; use update for partial rectangles", turing88.NativeWidth, turing88.NativeHeight, width, height))
		}
		if err := s.displayNativeImage(img, 0, 0); err != nil {
			return s.errorResponse(action, err)
		}
		return rpcResponse{OK: true, Action: "show", Message: "full image sent", Status: s.status()}
	case "update", "rect":
		if err := s.applyRequestedSettings(req); err != nil {
			return s.errorResponse(action, err)
		}
		img, err := loadRPCImage(req.Image.String())
		if err != nil {
			return s.errorResponse(action, err)
		}
		x, err := parseOptionalInt(req.PositionX, 0, "position_X")
		if err != nil {
			return s.errorResponse(action, err)
		}
		y, err := parseOptionalInt(req.PositionY, 0, "position_Y")
		if err != nil {
			return s.errorResponse(action, err)
		}
		if err := s.displayNativeImage(img, x, y); err != nil {
			return s.errorResponse(action, err)
		}
		return rpcResponse{OK: true, Action: "update", Message: "image sent", Status: s.status()}
	default:
		return s.errorResponse(action, fmt.Errorf("unknown action %q", action))
	}
}

func (s *screenService) setConfiguredPort(port string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.configuredPort = normalizePort(port)
	if s.configuredPort == "" {
		s.configuredPort = "AUTO"
	}
}

func (s *screenService) needsInitialize(requestedPort string, reset bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if reset || s.driver == nil || !s.initialized {
		return true
	}

	requestedPort = normalizePort(requestedPort)
	if requestedPort == "" || strings.EqualFold(requestedPort, "AUTO") {
		return false
	}

	return !strings.EqualFold(requestedPort, s.driver.PortName())
}

func (s *screenService) initialize(reset bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.driver != nil {
		_ = s.driver.Close()
		s.driver = nil
		s.initialized = false
	}

	driver, err := turing88.Open(s.configuredPort)
	if err != nil {
		return err
	}

	if reset {
		if err := driver.Reset(); err != nil {
			_ = driver.Close()
			return err
		}
	}

	displayID, err := driver.Initialize()
	if err != nil {
		_ = driver.Close()
		return err
	}
	if err := driver.ScreenOn(); err != nil {
		_ = driver.Close()
		return err
	}
	if err := driver.SetBrightness(s.brightness); err != nil {
		_ = driver.Close()
		return err
	}
	if err := driver.SetOrientation(s.orientation); err != nil {
		_ = driver.Close()
		return err
	}
	if err := sendDefaultSplash(driver); err != nil {
		_ = driver.Close()
		return err
	}

	s.driver = driver
	s.displayID = displayID
	s.initialized = true
	s.updatedAt = time.Now()
	return nil
}

func sendDefaultSplash(driver *turing88.Driver) error {
	data, err := defaultSplashNativeBGRA()
	if err != nil {
		return err
	}
	return driver.DisplayNativeBGRA(data, turing88.NativeWidth, turing88.NativeHeight, 0, 0)
}

func (s *screenService) applyRequestedSettings(req rpcRequest) error {
	if err := s.applyBrightness(req.Brightness); err != nil {
		return err
	}
	return s.applyOrientation(req.Orientation)
}

func (s *screenService) applyRequestedSettingsState(req rpcRequest) error {
	if req.Brightness.IsSet() {
		brightness, err := parseOptionalInt(req.Brightness, s.brightness, "brightness")
		if err != nil {
			return err
		}
		if brightness < 0 || brightness > 100 {
			return fmt.Errorf("brightness must be in [0,100], got %d", brightness)
		}
		s.setBrightnessState(brightness)
	}

	if req.Orientation.IsSet() {
		orientation, err := turing88.ParseOrientation(req.Orientation.String())
		if err != nil {
			return err
		}
		s.setOrientationState(orientation)
	}
	return nil
}

func (s *screenService) setBrightnessState(brightness int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.brightness = brightness
	s.updatedAt = time.Now()
}

func (s *screenService) setOrientationState(orientation turing88.Orientation) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.orientation = orientation
	s.updatedAt = time.Now()
}

func (s *screenService) applyBrightness(value rpcValue) error {
	if !value.IsSet() {
		return nil
	}
	brightness, err := parseOptionalInt(value, s.brightness, "brightness")
	if err != nil {
		return err
	}
	if brightness < 0 || brightness > 100 {
		return fmt.Errorf("brightness must be in [0,100], got %d", brightness)
	}

	return s.withDriver(func(d *turing88.Driver) error {
		if s.brightness == brightness {
			return nil
		}
		if err := d.SetBrightness(brightness); err != nil {
			return err
		}
		s.brightness = brightness
		s.updatedAt = time.Now()
		return nil
	})
}

func (s *screenService) applyOrientation(value rpcValue) error {
	if !value.IsSet() {
		return nil
	}
	orientation, err := turing88.ParseOrientation(value.String())
	if err != nil {
		return err
	}

	return s.withDriver(func(d *turing88.Driver) error {
		if s.orientation == orientation {
			return nil
		}
		if err := d.SetOrientation(orientation); err != nil {
			return err
		}
		s.orientation = orientation
		s.updatedAt = time.Now()
		return nil
	})
}

func (s *screenService) clearNative(c color.Color) error {
	img := image.NewRGBA(image.Rect(0, 0, turing88.NativeWidth, turing88.NativeHeight))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return s.displayNativeImage(img, 0, 0)
}

func (s *screenService) displayNativeImage(img image.Image, x, y int) error {
	return s.withDriver(func(d *turing88.Driver) error {
		if err := d.DisplayNativeImage(img, x, y); err != nil {
			return err
		}
		s.updatedAt = time.Now()
		return nil
	})
}

func (s *screenService) withDriver(fn func(*turing88.Driver) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.driver == nil || !s.initialized {
		return errors.New("driver is not initialized")
	}
	return fn(s.driver)
}

func (s *screenService) status() *serviceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	port := ""
	rom := 0
	if s.driver != nil {
		port = s.driver.PortName()
		rom = s.driver.ROMVersion()
	}

	return &serviceStatus{
		Initialized:    s.initialized,
		ConfiguredPort: s.configuredPort,
		Port:           port,
		DisplayID:      s.displayID,
		ROMVersion:     rom,
		NativeWidth:    turing88.NativeWidth,
		NativeHeight:   turing88.NativeHeight,
		PixelFormat:    "BGRA",
		Brightness:     s.brightness,
		Orientation:    s.orientation.String(),
		StartedAt:      s.startedAt.Format(time.RFC3339),
		UpdatedAt:      s.updatedAt.Format(time.RFC3339),
	}
}

func (s *screenService) errorResponse(action string, err error) rpcResponse {
	return rpcResponse{OK: false, Action: action, Error: err.Error(), Status: s.status()}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func listenTCPAutoDecrement(addr string) (net.Listener, string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = "127.0.0.1:60880"
	}

	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		listener, listenErr := net.Listen("tcp", addr)
		return listener, addr, listenErr
	}

	startPort, err := strconv.Atoi(portText)
	if err != nil || startPort == 0 {
		listener, listenErr := net.Listen("tcp", addr)
		return listener, addr, listenErr
	}

	var lastErr error
	for port := startPort; port >= 1; port-- {
		candidate := net.JoinHostPort(host, strconv.Itoa(port))
		listener, err := net.Listen("tcp", candidate)
		if err == nil {
			if port != startPort {
				fmt.Fprintf(os.Stderr, "tcp port %d unavailable, using %d\n", startPort, port)
			}
			return listener, candidate, nil
		}
		lastErr = err
		if !isRetryableListenError(err) {
			break
		}
	}

	return nil, "", fmt.Errorf("listen %s: %w", addr, lastErr)
}

func isRetryableListenError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "only one usage of each socket address") ||
		strings.Contains(msg, "forbidden by its access permissions")
}

func parseOptionalInt(value rpcValue, fallback int, name string) (int, error) {
	if !value.IsSet() {
		return fallback, nil
	}
	n, err := strconv.Atoi(value.String())
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return n, nil
}

func normalizePort(port string) string {
	port = strings.TrimSpace(port)
	if port == "" {
		return ""
	}
	if strings.EqualFold(port, "AUTO") {
		return "AUTO"
	}
	return port
}

func parseBool(value rpcValue) bool {
	if !value.IsSet() {
		return false
	}
	switch strings.ToLower(value.String()) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func parseRPCColor(value rpcValue) (color.RGBA, error) {
	if !value.IsSet() {
		return color.RGBA{A: 255}, nil
	}
	return parseHexColor(value.String())
}

func loadRPCImage(source string) (image.Image, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, errors.New("image is required")
	}

	if strings.HasPrefix(source, "data:image/") {
		return decodeDataURLImage(source)
	}
	if strings.HasPrefix(source, "base64:") {
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(source, "base64:"))
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		return img, err
	}

	f, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	return img, err
}

func readUploadedImage(r *http.Request) (image.Image, error) {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return readMultipartUploadedImage(r)
	}

	data, err := readLimitedBytes(r.Body)
	if err != nil {
		return nil, err
	}
	return decodeUploadedImageData(data)
}

func readMultipartUploadedImage(r *http.Request) (image.Image, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}

	var firstErr error
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		img, err := readMultipartImagePart(part)
		_ = part.Close()
		if err == nil {
			return img, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}

	if firstErr != nil {
		return nil, fmt.Errorf("no decodable image found in multipart form: %w", firstErr)
	}
	return nil, errors.New("no decodable image found in multipart form")
}

func readMultipartImagePart(part *multipart.Part) (image.Image, error) {
	data, err := readLimitedBytes(part)
	if err != nil {
		return nil, err
	}
	return decodeUploadedImageData(data)
}

func readLimitedBytes(reader io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(reader, maxImageUploadBytes+1)); err != nil {
		return nil, err
	}
	if buf.Len() > maxImageUploadBytes {
		return nil, fmt.Errorf("image upload exceeds %d bytes", maxImageUploadBytes)
	}
	return buf.Bytes(), nil
}

func decodeUploadedImageData(data []byte) (image.Image, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("image payload is empty")
	}
	if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
		return img, nil
	}
	return decodeUploadedImageText(string(data))
}

func decodeUploadedImageText(value string) (image.Image, error) {
	value = strings.TrimFunc(value, unicode.IsSpace)
	value = strings.Trim(value, `"'`)
	if value == "" {
		return nil, errors.New("image payload is empty")
	}
	if strings.HasPrefix(value, "data:image/") {
		return decodeDataURLImage(value)
	}
	value = strings.TrimPrefix(value, "base64:")

	value = stripBase64Whitespace(value)
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("image payload is neither a decodable image nor base64 image text")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func decodeDataURLImage(source string) (image.Image, error) {
	comma := strings.IndexByte(source, ',')
	if comma < 0 {
		return nil, errors.New("invalid data URL")
	}
	meta := source[:comma]
	if !strings.Contains(meta, ";base64") {
		return nil, errors.New("only base64 data URLs are supported")
	}
	data, err := base64.StdEncoding.DecodeString(stripBase64Whitespace(source[comma+1:]))
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func stripBase64Whitespace(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseImageQueryInt(r *http.Request, fallback int, names ...string) (int, error) {
	query := r.URL.Query()
	for _, name := range names {
		for _, value := range query[name] {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			n, err := strconv.Atoi(value)
			if err != nil {
				return 0, fmt.Errorf("%s must be an integer", name)
			}
			return n, nil
		}
	}
	return fallback, nil
}

func imageSizeOf(img image.Image) (int, int) {
	b := img.Bounds()
	return b.Dx(), b.Dy()
}
