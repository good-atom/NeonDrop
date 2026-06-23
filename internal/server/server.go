package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxUploadSize     = 512 << 20
	maxMessageLen     = 64 << 10
	sessionCookie     = "neondrop_session"
	deviceVisibleFor  = 10 * time.Minute
	deviceRetainedFor = 24 * time.Hour
)

//go:embed web/*
var webAssets embed.FS

type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"lastSeen"`
}

type Transfer struct {
	ID          string     `json:"id"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Kind        string     `json:"kind"`
	Text        string     `json:"text,omitempty"`
	Filename    string     `json:"filename,omitempty"`
	ContentType string     `json:"contentType,omitempty"`
	Size        int64      `json:"size,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	ReceivedAt  *time.Time `json:"receivedAt,omitempty"`
	path        string
}

type deviceState struct {
	Device
	Token       string
	Connections int
}

type event struct {
	Type     string    `json:"type"`
	Devices  []Device  `json:"devices,omitempty"`
	Transfer *Transfer `json:"transfer,omitempty"`
}

type Server struct {
	mu          sync.RWMutex
	devices     map[string]*deviceState
	tokens      map[string]string
	transfers   map[string]*Transfer
	subscribers map[string]map[chan event]struct{}
	uploadDir   string
	handler     http.Handler
	cancel      context.CancelFunc
	done        chan struct{}
	closeOnce   sync.Once
	allowedNets []*net.IPNet
}

func New(uploadDir string) (*Server, error) {
	if err := os.MkdirAll(uploadDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	allowedNets, err := discoverLocalNetworks()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("discover local networks: %w", err)
	}
	s := &Server{
		devices:     make(map[string]*deviceState),
		tokens:      make(map[string]string),
		transfers:   make(map[string]*Transfer),
		subscribers: make(map[string]map[chan event]struct{}),
		uploadDir:   uploadDir,
		cancel:      cancel,
		done:        make(chan struct{}),
		allowedNets: allowedNets,
	}
	s.handler = s.routes()
	go s.cleanupLoop(ctx)
	return s, nil
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		close(s.done)

		s.mu.Lock()
		defer s.mu.Unlock()
		for _, transfer := range s.transfers {
			if transfer.path != "" {
				_ = os.Remove(transfer.path)
			}
		}
	})
	return nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/devices/register", s.handleRegister)
	mux.HandleFunc("GET /api/devices", s.withAuth(s.handleDevices))
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/transfers", s.withAuth(s.handleTransfers))
	mux.HandleFunc("POST /api/messages", s.withAuth(s.handleMessage))
	mux.HandleFunc("POST /api/transfers", s.withAuth(s.handleFileUpload))
	mux.HandleFunc("GET /api/transfers/{id}/download", s.withAuth(s.handleDownload))
	mux.HandleFunc("POST /api/transfers/{id}/received", s.withAuth(s.handleReceived))
	mux.HandleFunc("DELETE /api/transfers/{id}", s.withAuth(s.handleDeleteTransfer))
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	staticFS, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	return s.localNetworkOnly(s.securityHeaders(s.sameOrigin(mux)))
}

func (s *Server) localNetworkOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			writeError(w, http.StatusForbidden, "local network access only")
			return
		}
		ip := net.ParseIP(host)
		if !s.isAllowedIP(ip) {
			writeError(w, http.StatusForbidden, "local network access only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) isAllowedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	ip = ip.To4()
	if ip == nil || !ip.IsPrivate() {
		return false
	}
	for _, subnet := range s.allowedNets {
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".js") ||
			strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" &&
				origin != "http://"+r.Host && origin != "https://"+r.Host {
				writeError(w, http.StatusForbidden, "cross-origin request blocked")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type registerRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.ID = cleanID(req.ID)
	req.Name = cleanLabel(req.Name, 40)
	req.Kind = cleanKind(req.Kind)
	if req.ID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "device id and name are required")
		return
	}

	now := time.Now().UTC()
	s.mu.Lock()
	if existing := s.devices[req.ID]; existing != nil {
		token := tokenFromRequest(r)
		if token == "" || token != existing.Token {
			s.mu.Unlock()
			writeError(w, http.StatusConflict, "device identity is already registered")
			return
		}
		existing.Name = req.Name
		existing.Kind = req.Kind
		existing.LastSeen = now
		device := existing.Device
		s.mu.Unlock()
		setSessionCookie(w, token)
		writeJSON(w, http.StatusOK, map[string]any{"device": device})
		s.broadcastPresence()
		return
	}

	token, err := randomID(32)
	if err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	s.devices[req.ID] = &deviceState{
		Device: Device{ID: req.ID, Name: req.Name, Kind: req.Kind, LastSeen: now},
		Token:  token,
	}
	s.tokens[token] = req.ID
	device := s.devices[req.ID].Device
	s.mu.Unlock()

	setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{"device": device})
	s.broadcastPresence()
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(deviceRetainedFor.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func tokenFromRequest(r *http.Request) string {
	if token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); token != "" {
		return token
	}
	cookie, err := r.Cookie(sessionCookie)
	if err == nil {
		return cookie.Value
	}
	return ""
}

func discoverLocalNetworks() ([]*net.IPNet, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var networks []*net.IPNet
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 ||
			iface.Flags&net.FlagPointToPoint != 0 || isTunnelInterface(iface.Name) {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, network, err := net.ParseCIDR(address.String())
			if err != nil || ip.To4() == nil || !ip.IsPrivate() {
				continue
			}
			network.IP = ip.Mask(network.Mask)
			networks = append(networks, network)
		}
	}
	return networks, nil
}

func isTunnelInterface(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{"utun", "tun", "tap", "wg", "ppp", "ipsec"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (s *Server) handleDevices(w http.ResponseWriter, _ *http.Request, _ string) {
	writeJSON(w, http.StatusOK, map[string]any{"devices": s.deviceSnapshot()})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := s.authenticateToken(tokenFromRequest(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid session")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}

	updates := make(chan event, 16)
	s.subscribe(deviceID, updates)
	defer s.unsubscribe(deviceID, updates)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	s.writeEvent(w, event{Type: "presence", Devices: s.deviceSnapshot()})
	for _, transfer := range s.transferSnapshot(deviceID) {
		if transfer.To == deviceID && transfer.ReceivedAt == nil {
			copy := transfer
			s.writeEvent(w, event{Type: transfer.Kind, Transfer: &copy})
		}
	}
	flusher.Flush()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case update := <-updates:
			s.writeEvent(w, update)
			flusher.Flush()
		case <-ticker.C:
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-s.done:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleTransfers(w http.ResponseWriter, _ *http.Request, deviceID string) {
	writeJSON(w, http.StatusOK, map[string]any{"transfers": s.transferSnapshot(deviceID)})
}

type messageRequest struct {
	TargetID string `json:"targetId"`
	Text     string `json:"text"`
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request, deviceID string) {
	var req messageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.TargetID = cleanID(req.TargetID)
	req.Text = strings.TrimSpace(req.Text)
	if req.TargetID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "recipient and text are required")
		return
	}
	if len(req.Text) > maxMessageLen {
		writeError(w, http.StatusRequestEntityTooLarge, "text is too large")
		return
	}
	if !s.deviceExists(req.TargetID) {
		writeError(w, http.StatusNotFound, "recipient not found")
		return
	}

	transfer, err := s.newTransfer(deviceID, req.TargetID, "message")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create transfer")
		return
	}
	transfer.Text = req.Text
	s.storeTransfer(transfer)
	s.publish(req.TargetID, event{Type: "message", Transfer: transfer})
	writeJSON(w, http.StatusCreated, transfer)
}

func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request, deviceID string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize+(2<<20))
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload or file is too large")
		return
	}

	targetID := cleanID(r.FormValue("targetId"))
	if !s.deviceExists(targetID) {
		writeError(w, http.StatusNotFound, "recipient not found")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	transfer, err := s.newTransfer(deviceID, targetID, "file")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create transfer")
		return
	}
	transfer.Filename = cleanFilename(header.Filename)
	transfer.ContentType = cleanContentType(header.Header.Get("Content-Type"))
	transfer.path = filepath.Join(s.uploadDir, transfer.ID)

	dst, err := os.OpenFile(transfer.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not store file")
		return
	}
	size, copyErr := io.Copy(dst, io.LimitReader(file, maxUploadSize+1))
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil || size > maxUploadSize {
		_ = os.Remove(transfer.path)
		writeError(w, http.StatusRequestEntityTooLarge, "file is too large")
		return
	}

	transfer.Size = size
	s.storeTransfer(transfer)
	s.publish(targetID, event{Type: "file", Transfer: transfer})
	writeJSON(w, http.StatusCreated, transfer)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request, deviceID string) {
	transfer, ok := s.getTransfer(r.PathValue("id"))
	if !ok || transfer.Kind != "file" {
		writeError(w, http.StatusNotFound, "transfer not found")
		return
	}
	if transfer.To != deviceID && transfer.From != deviceID {
		writeError(w, http.StatusForbidden, "transfer belongs to another device")
		return
	}

	file, err := os.Open(transfer.path)
	if err != nil {
		writeError(w, http.StatusGone, "file is no longer available")
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", transfer.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", transfer.Size))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": transfer.Filename,
	}))
	_, _ = io.Copy(w, file)
}

func (s *Server) handleReceived(w http.ResponseWriter, r *http.Request, deviceID string) {
	id := r.PathValue("id")
	s.mu.Lock()
	transfer := s.transfers[id]
	if transfer == nil {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "transfer not found")
		return
	}
	if transfer.To != deviceID {
		s.mu.Unlock()
		writeError(w, http.StatusForbidden, "only the recipient can confirm receipt")
		return
	}
	receivedAt := time.Now().UTC()
	transfer.ReceivedAt = &receivedAt
	copy := *transfer
	s.mu.Unlock()

	s.publish(transfer.From, event{Type: "transfer_updated", Transfer: &copy})
	writeJSON(w, http.StatusOK, &copy)
}

func (s *Server) handleDeleteTransfer(w http.ResponseWriter, r *http.Request, deviceID string) {
	id := r.PathValue("id")
	s.mu.Lock()
	transfer := s.transfers[id]
	if transfer == nil {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "transfer not found")
		return
	}
	if transfer.To != deviceID && transfer.From != deviceID {
		s.mu.Unlock()
		writeError(w, http.StatusForbidden, "transfer belongs to another device")
		return
	}
	delete(s.transfers, id)
	s.mu.Unlock()

	if transfer.path != "" {
		_ = os.Remove(transfer.path)
	}
	w.WriteHeader(http.StatusNoContent)
}

type authedHandler func(http.ResponseWriter, *http.Request, string)

func (s *Server) withAuth(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceID, ok := s.authenticateToken(tokenFromRequest(r))
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid session")
			return
		}
		next(w, r, deviceID)
	}
}

func (s *Server) authenticateToken(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	deviceID, ok := s.tokens[token]
	return deviceID, ok
}

func (s *Server) subscribe(deviceID string, updates chan event) {
	s.mu.Lock()
	state := s.devices[deviceID]
	if state != nil {
		state.Connections++
		state.Online = true
		state.LastSeen = time.Now().UTC()
	}
	if s.subscribers[deviceID] == nil {
		s.subscribers[deviceID] = make(map[chan event]struct{})
	}
	s.subscribers[deviceID][updates] = struct{}{}
	s.mu.Unlock()
	s.broadcastPresence()
}

func (s *Server) unsubscribe(deviceID string, updates chan event) {
	s.mu.Lock()
	delete(s.subscribers[deviceID], updates)
	if state := s.devices[deviceID]; state != nil {
		if state.Connections > 0 {
			state.Connections--
		}
		state.Online = state.Connections > 0
		state.LastSeen = time.Now().UTC()
	}
	s.mu.Unlock()
	s.broadcastPresence()
}

func (s *Server) publish(deviceID string, update event) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for subscriber := range s.subscribers[deviceID] {
		select {
		case subscriber <- update:
		default:
		}
	}
}

func (s *Server) broadcastPresence() {
	update := event{Type: "presence", Devices: s.deviceSnapshot()}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, subscribers := range s.subscribers {
		for subscriber := range subscribers {
			select {
			case subscriber <- update:
			default:
			}
		}
	}
}

func (s *Server) writeEvent(w io.Writer, update event) {
	payload, _ := json.Marshal(update)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
}

func (s *Server) deviceSnapshot() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().UTC().Add(-deviceVisibleFor)
	devices := make([]Device, 0, len(s.devices))
	for _, state := range s.devices {
		if !state.Online && state.LastSeen.Before(cutoff) {
			continue
		}
		devices = append(devices, state.Device)
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Online != devices[j].Online {
			return devices[i].Online
		}
		return devices[i].Name < devices[j].Name
	})
	return devices
}

func (s *Server) transferSnapshot(deviceID string) []Transfer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	transfers := make([]Transfer, 0)
	for _, transfer := range s.transfers {
		if transfer.To == deviceID || transfer.From == deviceID {
			copy := *transfer
			copy.path = ""
			transfers = append(transfers, copy)
		}
	}
	sort.Slice(transfers, func(i, j int) bool {
		return transfers[i].CreatedAt.After(transfers[j].CreatedAt)
	})
	if len(transfers) > 100 {
		transfers = transfers[:100]
	}
	return transfers
}

func (s *Server) newTransfer(from, to, kind string) (*Transfer, error) {
	id, err := randomID(18)
	if err != nil {
		return nil, err
	}
	return &Transfer{
		ID:        id,
		From:      from,
		To:        to,
		Kind:      kind,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (s *Server) storeTransfer(transfer *Transfer) {
	s.mu.Lock()
	s.transfers[transfer.ID] = transfer
	s.mu.Unlock()
}

func (s *Server) getTransfer(id string) (Transfer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	transfer := s.transfers[id]
	if transfer == nil {
		return Transfer{}, false
	}
	copy := *transfer
	return copy, true
}

func (s *Server) deviceExists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.devices[id]
	return ok
}

func (s *Server) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now().UTC()
			s.cleanupExpired(now.Add(-24*time.Hour), now.Add(-deviceRetainedFor))
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) cleanupExpired(transferBefore, deviceBefore time.Time) {
	var paths []string
	s.mu.Lock()
	for id, transfer := range s.transfers {
		if transfer.CreatedAt.Before(transferBefore) {
			if transfer.path != "" {
				paths = append(paths, transfer.path)
			}
			delete(s.transfers, id)
		}
	}
	for id, state := range s.devices {
		if !state.Online && state.LastSeen.Before(deviceBefore) {
			delete(s.tokens, state.Token)
			delete(s.subscribers, id)
			delete(s.devices, id)
		}
	}
	s.mu.Unlock()
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func cleanID(value string) string {
	if len(value) < 8 || len(value) > 80 {
		return ""
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '-' && r != '_' {
			return ""
		}
	}
	return value
}

func cleanLabel(value string, limit int) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
	if len([]rune(value)) > limit {
		value = string([]rune(value)[:limit])
	}
	return value
}

func cleanKind(value string) string {
	switch value {
	case "desktop", "laptop", "phone", "tablet":
		return value
	default:
		return "device"
	}
}

func cleanFilename(value string) string {
	value = filepath.Base(strings.ReplaceAll(value, "\\", "/"))
	value = cleanLabel(value, 160)
	if value == "" || value == "." {
		return "file"
	}
	return value
}

func cleanContentType(value string) string {
	value = strings.TrimSpace(strings.Split(value, ";")[0])
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "application/octet-stream"
	}
	return value
}

func randomID(bytes int) (string, error) {
	data := make([]byte, bytes)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeJSON(r *http.Request, value any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid JSON request")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
