package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	cfg   *Config
	cloud *Cloud
	local *LocalClient
}

func main() {
	defPath, err := defaultConfigPath()
	if err != nil {
		log.Fatalf("ホームディレクトリを特定できません: %v", err)
	}
	configPath := flag.String("config", defPath, "設定ファイルのパス")
	port := flag.Int("port", 0, "待ち受けポート（設定ファイルより優先）")
	bind := flag.String("bind", "", "待ち受けアドレス（設定ファイルより優先）")
	logPath := flag.String("log", "", "ログの出力先ファイル（省略時は標準エラー出力）")
	flag.Parse()

	// The Windows autostart build runs without a console, so it needs
	// somewhere to put its output.
	if *logPath != "" {
		if err := os.MkdirAll(filepath.Dir(*logPath), 0o700); err == nil {
			if f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				defer f.Close()
				log.SetOutput(f)
			}
		}
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("設定の読み込みに失敗しました: %v", err)
	}
	// Flags win over the file, but are not written back to it.
	if *port != 0 || *bind != "" {
		_ = cfg.Update(func(s *Settings) {
			if *port != 0 {
				s.Port = *port
			}
			if *bind != "" {
				s.Bind = *bind
			}
		})
	}
	snap := cfg.Snapshot()

	s := &Server{cfg: cfg, cloud: NewCloud(cfg), local: NewLocalClient(cfg)}

	addr := net.JoinHostPort(snap.Bind, strconv.Itoa(snap.Port))
	srv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("%s を開けません: %v\n別のインスタンスが起動していませんか？ `-port 8081` で変更できます。", addr, err)
	}

	log.Printf("remo-panel: http://localhost:%d/ で待ち受け中", snap.Port)
	log.Printf("設定ファイル: %s", cfg.Path())
	if snap.Bind != "127.0.0.1" && snap.Bind != "localhost" {
		if snap.AccessKey == "" {
			log.Printf("警告: %s で待ち受けています。同一LANの誰でも家電を操作できます。設定画面でアクセスキーを設定してください。", snap.Bind)
		}
		for _, ip := range localIPv4s() {
			log.Printf("LAN から: http://%s:%d/", ip, snap.Port)
		}
	}
	if snap.Token == "" {
		log.Printf("アクセストークン未設定。ブラウザで開いて設定画面から登録してください。")
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("サーバが停止しました: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("終了します")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func localIPv4s() []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if v4 := ipnet.IP.To4(); v4 != nil {
				out = append(out, v4.String())
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/settings", s.handleSaveSettings)

	mux.HandleFunc("POST /api/signals/{id}/send", s.handleSendSignal)
	mux.HandleFunc("POST /api/appliances/{id}/light", s.handleLight)
	mux.HandleFunc("POST /api/appliances/{id}/tv", s.handleTV)
	mux.HandleFunc("POST /api/appliances/{id}/aircon", s.handleAircon)

	mux.HandleFunc("POST /api/local/capture", s.handleLocalCapture)
	mux.HandleFunc("POST /api/local/signals", s.handleLocalSave)
	mux.HandleFunc("POST /api/local/signals/{id}/send", s.handleLocalSend)
	mux.HandleFunc("POST /api/local/signals/{id}/delete", s.handleLocalDelete)
	mux.HandleFunc("POST /api/local/test", s.handleLocalTest)

	return s.withAuth(s.withLogging(mux))
}

// withAuth is a no-op while bound to loopback. Once the panel is exposed to
// the LAN, an access key is the only thing standing between a guest on the
// Wi-Fi and the air conditioner.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := s.cfg.Snapshot().AccessKey
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		given := r.Header.Get("X-Access-Key")
		if given == "" {
			given = r.URL.Query().Get("k")
		}
		if given == "" {
			if c, err := r.Cookie("remo_key"); err == nil {
				given = c.Value
			}
		}
		if subtle.ConstantTimeCompare([]byte(given), []byte(key)) != 1 {
			http.Error(w, "アクセスキーが必要です (?k=... を付けてアクセスしてください)", http.StatusUnauthorized)
			return
		}
		// Remember it so in-page fetches and reloads do not need ?k= again.
		http.SetCookie(w, &http.Cookie{
			Name: "remo_key", Value: key, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400 * 365,
		})
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			start := time.Now()
			next.ServeHTTP(w, r)
			log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr maps upstream failures onto meaningful status codes so the UI can
// say "レート制限" rather than a generic error.
func writeErr(w http.ResponseWriter, err error) {
	var apiErr *APIError
	status := http.StatusBadGateway
	msg := err.Error()

	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			status = http.StatusUnauthorized
			msg = "アクセストークンが無効か、権限が足りません。設定画面で再登録してください。"
		case http.StatusTooManyRequests:
			status = http.StatusTooManyRequests
			msg = "Nature API のレート制限に達しました（5分あたり30リクエスト）。しばらく待ってから再試行してください。"
		case http.StatusNoContent:
			status = http.StatusConflict
			msg = apiErr.Body
		default:
			status = apiErr.Status
			if status < 400 {
				status = http.StatusBadGateway
			}
		}
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	return dec.Decode(v)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type stateResponse struct {
	Configured    bool          `json:"configured"`
	RemoLocalHost string        `json:"remoLocalHost"`
	Bind          string        `json:"bind"`
	HasAccessKey  bool          `json:"hasAccessKey"`
	Appliances    []Appliance   `json:"appliances"`
	LocalSignals  []LocalSignal `json:"localSignals"`
	RateLimit     RateLimit     `json:"rateLimit"`
	// Stale means the list could not be refreshed and is a fallback copy.
	Stale bool   `json:"stale"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	snap := s.cfg.Snapshot()
	resp := stateResponse{
		Configured:    snap.Token != "",
		RemoLocalHost: snap.RemoLocalHost,
		Bind:          snap.Bind,
		HasAccessKey:  snap.AccessKey != "",
		LocalSignals:  snap.LocalSignals,
	}
	if resp.LocalSignals == nil {
		resp.LocalSignals = []LocalSignal{}
	}

	if snap.Token != "" {
		maxAge := 60 * time.Second
		if r.URL.Query().Get("refresh") == "1" {
			maxAge = 0
		}
		list, stale, err := s.cloud.Appliances(r.Context(), maxAge)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests {
				resp.Error = "レート制限中です。しばらく待ってから再読み込みしてください。"
			} else {
				resp.Error = err.Error()
			}
		}
		resp.Appliances = list
		resp.Stale = stale
	}
	if resp.Appliances == nil {
		resp.Appliances = []Appliance{}
	}
	resp.RateLimit = s.cloud.RateLimit()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token         *string `json:"token"`
		RemoLocalHost *string `json:"remoLocalHost"`
		Bind          *string `json:"bind"`
		AccessKey     *string `json:"accessKey"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストが不正です"})
		return
	}

	tokenChanged := false
	err := s.cfg.Update(func(c *Settings) {
		if body.Token != nil && strings.TrimSpace(*body.Token) != "" {
			c.Token = strings.TrimSpace(*body.Token)
			tokenChanged = true
		}
		if body.RemoLocalHost != nil {
			c.RemoLocalHost = strings.TrimSpace(*body.RemoLocalHost)
		}
		if body.Bind != nil && strings.TrimSpace(*body.Bind) != "" {
			c.Bind = strings.TrimSpace(*body.Bind)
		}
		if body.AccessKey != nil {
			c.AccessKey = strings.TrimSpace(*body.AccessKey)
		}
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "設定の保存に失敗: " + err.Error()})
		return
	}

	if tokenChanged {
		// Drop the cache so the next load reflects the new account.
		s.cloud.mu.Lock()
		s.cloud.cache, s.cloud.cacheAt = nil, time.Time{}
		s.cloud.mu.Unlock()
	}

	snap := s.cfg.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"bindChanged":  body.Bind != nil,
		"needsRestart": body.Bind != nil,
		"bind":         snap.Bind,
	})
}

func (s *Server) handleSendSignal(w http.ResponseWriter, r *http.Request) {
	if err := s.cloud.SendSignal(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rateLimit": s.cloud.RateLimit()})
}

func (s *Server) handleButtonKind(w http.ResponseWriter, r *http.Request, kind string) {
	var body struct {
		Button string `json:"button"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Button == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "button が指定されていません"})
		return
	}
	if err := s.cloud.SendButton(r.Context(), kind, r.PathValue("id"), body.Button); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rateLimit": s.cloud.RateLimit()})
}

func (s *Server) handleLight(w http.ResponseWriter, r *http.Request) {
	s.handleButtonKind(w, r, "light")
}

func (s *Server) handleTV(w http.ResponseWriter, r *http.Request) {
	s.handleButtonKind(w, r, "tv")
}

func (s *Server) handleAircon(w http.ResponseWriter, r *http.Request) {
	var patch AirconPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストが不正です"})
		return
	}
	settings, err := s.cloud.SetAircon(r.Context(), r.PathValue("id"), patch)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "settings": settings, "rateLimit": s.cloud.RateLimit(),
	})
}

// --- Local API -------------------------------------------------------------

func (s *Server) handleLocalTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host string `json:"host"`
	}
	_ = decodeJSON(r, &body)
	if h := strings.TrimSpace(body.Host); h != "" {
		// Probe the candidate host without persisting it yet.
		probe := NewLocalClientForHost(h)
		if _, err := probe.Capture(r.Context()); err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNoContent {
				// Reachable; it simply has no signal buffered. That is a pass.
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Remo に接続できました。"})
				return
			}
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Remo に接続できました。"})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ホスト名が空です"})
}

func (s *Server) handleLocalCapture(w http.ResponseWriter, r *http.Request) {
	ir, err := s.local.Capture(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ir": ir})
}

func (s *Server) handleLocalSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string          `json:"name"`
		IR   json.RawMessage `json:"ir"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Name) == "" || len(body.IR) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "名前と信号データの両方が必要です"})
		return
	}
	sig := LocalSignal{
		ID:   fmt.Sprintf("ls%d", time.Now().UnixNano()),
		Name: strings.TrimSpace(body.Name),
		IR:   body.IR,
	}
	if err := s.cfg.Update(func(c *Settings) { c.LocalSignals = append(c.LocalSignals, sig) }); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存に失敗: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "signal": sig})
}

func (s *Server) handleLocalSend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var ir json.RawMessage
	for _, sig := range s.cfg.Snapshot().LocalSignals {
		if sig.ID == id {
			ir = sig.IR
			break
		}
	}
	if ir == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "信号が見つかりません"})
		return
	}
	if err := s.local.Send(r.Context(), ir); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLocalDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.cfg.Update(func(c *Settings) {
		out := c.LocalSignals[:0]
		for _, sig := range c.LocalSignals {
			if sig.ID != id {
				out = append(out, sig)
			}
		}
		c.LocalSignals = out
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "削除に失敗: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
