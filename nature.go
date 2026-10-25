package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cloudBase is overridable so the test harness can point at a stub server.
var cloudBase = func() string {
	if v := os.Getenv("REMO_PANEL_CLOUD_BASE"); v != "" {
		return v
	}
	return "https://api.nature.global"
}()

// ---------------------------------------------------------------------------
// Types mirroring the subset of the Nature Cloud API we actually use.
// Full spec: https://swagger.nature.global
// ---------------------------------------------------------------------------

type Button struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Label string `json:"label"`
}

type Signal struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
}

type AirconSettings struct {
	Temp     string `json:"temp"`
	TempUnit string `json:"temp_unit"`
	Mode     string `json:"mode"`
	Vol      string `json:"vol"`
	Dir      string `json:"dir"`
	DirH     string `json:"dirh"`
	Button   string `json:"button"`
}

type AirconRangeMode struct {
	Temp []string `json:"temp"`
	Vol  []string `json:"vol"`
	Dir  []string `json:"dir"`
	DirH []string `json:"dirh"`
}

type Aircon struct {
	Range struct {
		Modes        map[string]AirconRangeMode `json:"modes"`
		FixedButtons []string                   `json:"fixedButtons"`
	} `json:"range"`
	TempUnit string `json:"tempUnit"`
}

type SensorValue struct {
	Val       float64 `json:"val"`
	CreatedAt string  `json:"created_at"`
}

type Device struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	NewestEvents map[string]SensorValue `json:"newest_events"`
}

type Appliance struct {
	ID       string          `json:"id"`
	Nickname string          `json:"nickname"`
	Image    string          `json:"image"`
	Type     string          `json:"type"`
	Device   *Device         `json:"device"`
	Settings *AirconSettings `json:"settings"`
	Aircon   *Aircon         `json:"aircon"`
	Signals  []Signal        `json:"signals"`
	TV       *struct {
		Buttons []Button `json:"buttons"`
		State   *struct {
			Input string `json:"input"`
		} `json:"state"`
	} `json:"tv"`
	Light *struct {
		Buttons []Button `json:"buttons"`
		State   *struct {
			Brightness string `json:"brightness"`
			Power      string `json:"power"`
			LastButton string `json:"last_button"`
		} `json:"state"`
	} `json:"light"`
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// APIError carries the upstream status so handlers can pass 429 and 401
// through to the browser instead of flattening everything into 500.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Nature API %d: %s", e.Status, strings.TrimSpace(e.Body))
}

type RateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
}

type Cloud struct {
	cfg *Config
	hc  *http.Client

	mu        sync.RWMutex
	cache     []Appliance
	cacheAt   time.Time
	inflight  *listFetch
	rateLimit RateLimit
}

// listFetch is one upstream GET /1/appliances that concurrent callers share.
type listFetch struct {
	done chan struct{}
	list []Appliance
	err  error
}

func NewCloud(cfg *Config) *Cloud {
	return &Cloud{
		cfg: cfg,
		hc:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Cloud) RateLimit() RateLimit {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rateLimit
}

func (c *Cloud) noteRateLimit(h http.Header) {
	lim, _ := strconv.Atoi(h.Get("X-Rate-Limit-Limit"))
	rem, err := strconv.Atoi(h.Get("X-Rate-Limit-Remaining"))
	if err != nil {
		return
	}
	var reset time.Time
	if s := h.Get("X-Rate-Limit-Reset"); s != "" {
		if secs, err := strconv.ParseInt(s, 10, 64); err == nil {
			reset = time.Unix(secs, 0)
		}
	}
	c.mu.Lock()
	c.rateLimit = RateLimit{Limit: lim, Remaining: rem, Reset: reset}
	c.mu.Unlock()
}

func (c *Cloud) do(ctx context.Context, method, path string, form url.Values, out any) error {
	token := c.cfg.Snapshot().Token
	if token == "" {
		return &APIError{Status: http.StatusUnauthorized, Body: "アクセストークンが設定されていません"}
	}

	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, cloudBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("Nature API に接続できません: %w", err)
	}
	defer resp.Body.Close()
	c.noteRateLimit(resp.Header)

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("レスポンスの解析に失敗: %w", err)
		}
	}
	return nil
}

// Appliances returns the appliance list, serving a cached copy when it is
// fresh. The cache matters: the Cloud API throttles above 30 requests per
// 5 minutes, and a UI that refetched on every click would exhaust that fast.
//
// Callers that arrive while a fetch is already under way wait for it instead
// of starting their own. Several tabs or devices tend to refresh at the same
// moment, and without this each of them went upstream when the cache expired.
//
// The bool reports *stale* data — a fallback to the last known list after the
// API call failed — not an ordinary cache hit, which is the normal case and
// nothing the user needs to be told about.
func (c *Cloud) Appliances(ctx context.Context, maxAge time.Duration) ([]Appliance, bool, error) {
	c.mu.Lock()
	cached, at := c.cache, c.cacheAt
	if cached != nil && maxAge > 0 && time.Since(at) < maxAge {
		c.mu.Unlock()
		return cached, false, nil
	}
	f := c.inflight
	if f == nil {
		f = &listFetch{done: make(chan struct{})}
		c.inflight = f
		// Detach from this caller's context: others may be waiting on the
		// result, and one closed tab must not fail the fetch for all of them.
		// The HTTP client's own timeout still bounds it.
		go c.fetchAppliances(context.WithoutCancel(ctx), f)
	}
	c.mu.Unlock()

	select {
	case <-f.done:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	if f.err != nil {
		// Serve stale data rather than an empty panel when we are throttled
		// or briefly offline; the buttons still work.
		if cached != nil {
			return cached, true, nil
		}
		return nil, false, f.err
	}
	return f.list, false, nil
}

func (c *Cloud) fetchAppliances(ctx context.Context, f *listFetch) {
	var list []Appliance
	err := c.do(ctx, http.MethodGet, "/1/appliances", nil, &list)

	c.mu.Lock()
	if err == nil {
		c.cache, c.cacheAt = list, time.Now()
	}
	c.inflight = nil
	c.mu.Unlock()

	f.list, f.err = list, err
	close(f.done)
}

func (c *Cloud) findAppliance(id string) (Appliance, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, a := range c.cache {
		if a.ID == id {
			return a, true
		}
	}
	return Appliance{}, false
}

// updateCachedSettings swaps in a modified copy of the list rather than
// editing it in place: Appliances hands the cached slice out without a lock,
// and a handler may be encoding it to JSON at this very moment.
func (c *Cloud) updateCachedSettings(id string, s AirconSettings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.cache {
		if c.cache[i].ID == id {
			next := append([]Appliance(nil), c.cache...)
			cp := s
			next[i].Settings = &cp
			c.cache = next
			return
		}
	}
}

func (c *Cloud) SendSignal(ctx context.Context, signalID string) error {
	return c.do(ctx, http.MethodPost, "/1/signals/"+url.PathEscape(signalID)+"/send", url.Values{}, nil)
}

func (c *Cloud) SendButton(ctx context.Context, kind, applianceID, button string) error {
	form := url.Values{"button": {button}}
	path := "/1/appliances/" + url.PathEscape(applianceID) + "/" + kind
	return c.do(ctx, http.MethodPost, path, form, nil)
}

// AirconPatch is a partial update. Fields left nil keep their current value,
// which we read from the cached appliance — the API requires the full set.
type AirconPatch struct {
	Temperature   *string `json:"temperature"`
	OperationMode *string `json:"operation_mode"`
	AirVolume     *string `json:"air_volume"`
	AirDirection  *string `json:"air_direction"`
	AirDirectionH *string `json:"air_direction_h"`
	Button        *string `json:"button"`
}

func (c *Cloud) SetAircon(ctx context.Context, applianceID string, p AirconPatch) (AirconSettings, error) {
	// The API requires the full parameter set, so we need the appliance's
	// current settings. The cache is normally warm (the page just listed it),
	// but a restarted server serving an already-open tab has an empty one —
	// populate it rather than failing the click.
	app, ok := c.findAppliance(applianceID)
	if !ok {
		if _, _, err := c.Appliances(ctx, 0); err != nil {
			return AirconSettings{}, err
		}
		if app, ok = c.findAppliance(applianceID); !ok {
			return AirconSettings{}, &APIError{
				Status: http.StatusNotFound,
				Body:   "家電が見つかりません（一覧を再読み込みしてください）",
			}
		}
	}
	cur := AirconSettings{}
	if app.Settings != nil {
		cur = *app.Settings
	}
	if cur.TempUnit == "" && app.Aircon != nil {
		cur.TempUnit = app.Aircon.TempUnit
	}

	pick := func(p *string, fallback string) string {
		if p != nil {
			return *p
		}
		return fallback
	}

	mode := pick(p.OperationMode, cur.Mode)
	temp := pick(p.Temperature, cur.Temp)
	vol := pick(p.AirVolume, cur.Vol)
	dir := pick(p.AirDirection, cur.Dir)
	dirh := pick(p.AirDirectionH, cur.DirH)

	// Switching mode can invalidate the current temperature (e.g. "dry" often
	// has a different or empty range). Clamp into the new mode's range.
	if p.OperationMode != nil && p.Temperature == nil && app.Aircon != nil {
		if rng, ok := app.Aircon.Range.Modes[mode]; ok {
			temp = clampToRange(temp, rng.Temp)
			vol = clampMember(vol, rng.Vol)
			dir = clampMember(dir, rng.Dir)
			dirh = clampMember(dirh, rng.DirH)
		}
	}

	form := url.Values{
		"temperature":      {temp},
		"temperature_unit": {cur.TempUnit},
		"operation_mode":   {mode},
		"air_volume":       {vol},
		"air_direction":    {dir},
		"air_direction_h":  {dirh},
		"button":           {pick(p.Button, "")},
	}

	var out AirconSettings
	path := "/1/appliances/" + url.PathEscape(applianceID) + "/aircon_settings"
	if err := c.do(ctx, http.MethodPost, path, form, &out); err != nil {
		return AirconSettings{}, err
	}
	c.updateCachedSettings(applianceID, out)
	return out, nil
}

// clampToRange keeps v if the mode accepts it, otherwise picks the numerically
// closest accepted value (falling back to the middle of the range).
func clampToRange(v string, allowed []string) string {
	if len(allowed) == 0 {
		return ""
	}
	for _, a := range allowed {
		if a == v {
			return v
		}
	}
	want, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return allowed[len(allowed)/2]
	}
	best, bestDiff := allowed[len(allowed)/2], math.MaxFloat64
	for _, a := range allowed {
		f, err := strconv.ParseFloat(a, 64)
		if err != nil {
			continue
		}
		if d := math.Abs(f - want); d < bestDiff {
			best, bestDiff = a, d
		}
	}
	return best
}

func clampMember(v string, allowed []string) string {
	if len(allowed) == 0 {
		return ""
	}
	for _, a := range allowed {
		if a == v {
			return v
		}
	}
	return allowed[0]
}

// ---------------------------------------------------------------------------
// Local API — talks straight to the Remo over the LAN.
// No auth, no rate limit, no round trip to Nature's servers.
// ---------------------------------------------------------------------------

type LocalClient struct {
	cfg *Config
	// hostOverride lets the settings screen probe a candidate address before
	// it is committed to the config file.
	hostOverride string
	hc           *http.Client
}

func NewLocalClient(cfg *Config) *LocalClient {
	return &LocalClient{cfg: cfg, hc: &http.Client{Timeout: 6 * time.Second}}
}

func NewLocalClientForHost(host string) *LocalClient {
	return &LocalClient{hostOverride: host, hc: &http.Client{Timeout: 6 * time.Second}}
}

var errNoLocalHost = errors.New("Remo のローカルアドレスが設定されていません")

func (l *LocalClient) base() (string, error) {
	h := strings.TrimSpace(l.hostOverride)
	if h == "" && l.cfg != nil {
		h = strings.TrimSpace(l.cfg.Snapshot().RemoLocalHost)
	}
	if h == "" {
		return "", errNoLocalHost
	}
	h = strings.TrimPrefix(strings.TrimPrefix(h, "http://"), "https://")
	return "http://" + strings.TrimSuffix(h, "/"), nil
}

// Capture fetches the most recently received IR signal — i.e. what you just
// pointed your physical remote at the Remo and pressed.
func (l *LocalClient) Capture(ctx context.Context) (json.RawMessage, error) {
	base, err := l.base()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/messages", nil)
	if err != nil {
		return nil, err
	}
	// Required by the Local API on every endpoint.
	req.Header.Set("X-Requested-With", "remo-panel")
	req.Header.Set("Accept", "application/json")

	resp, err := l.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Remo に接続できません (%s): %w", base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var probe struct {
		Data []int `json:"data"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("Remo の応答を解析できません: %w", err)
	}
	if len(probe.Data) == 0 {
		return nil, &APIError{Status: http.StatusNoContent,
			Body: "赤外線信号が受信されていません。Remo に向けてリモコンのボタンを押してから、もう一度取得してください。"}
	}
	return json.RawMessage(raw), nil
}

func (l *LocalClient) Send(ctx context.Context, ir json.RawMessage) error {
	base, err := l.base()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/messages", bytes.NewReader(ir))
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "remo-panel")
	req.Header.Set("Content-Type", "application/json")
	// The Remo does not understand "Expect: 100-continue". Go only sends it
	// when asked to, but set ContentLength explicitly so it never considers it.
	req.ContentLength = int64(len(ir))

	resp, err := l.hc.Do(req)
	if err != nil {
		return fmt.Errorf("Remo に接続できません (%s): %w", base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	return nil
}
