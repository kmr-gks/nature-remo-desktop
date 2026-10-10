package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LocalSignal is an IR signal captured directly from the Remo's Local API.
// IR holds the raw {"freq":..,"data":[..],"format":"us"} payload that the
// device accepts on POST /messages.
type LocalSignal struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	IR   json.RawMessage `json:"ir"`
}

// Settings is the on-disk shape. It is kept separate from Config so that a
// snapshot can be copied freely without dragging the mutex along.
type Settings struct {
	// Token is the Nature Cloud API access token issued at home.nature.global.
	Token string `json:"token"`
	// Bind is the listen address. 127.0.0.1 keeps the panel to this machine;
	// 0.0.0.0 exposes it to the LAN (phones, the other PC) — see AccessKey.
	Bind string `json:"bind"`
	Port int    `json:"port"`
	// AccessKey, when set, is required as ?k=... or an X-Access-Key header.
	// Only meaningful together with a non-loopback Bind.
	AccessKey string `json:"accessKey"`
	// RemoLocalHost is the Remo's hostname or IP on the LAN, e.g.
	// "Remo-1A2B3C.local" or "192.168.1.23". Empty disables the local fast path.
	RemoLocalHost string        `json:"remoLocalHost"`
	LocalSignals  []LocalSignal `json:"localSignals"`
}

type Config struct {
	mu   sync.Mutex
	s    Settings
	path string
}

func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".remo-panel", "config.json"), nil
}

func LoadConfig(path string) (*Config, error) {
	c := &Config{
		s:    Settings{Bind: "127.0.0.1", Port: 8080},
		path: path,
	}

	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// First run: materialise a default file so the user can find it.
		if err := c.Save(); err != nil {
			return nil, fmt.Errorf("could not create %s: %w", path, err)
		}
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &c.s); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	if c.s.Bind == "" {
		c.s.Bind = "127.0.0.1"
	}
	if c.s.Port == 0 {
		c.s.Port = 8080
	}
	return c, nil
}

// Save writes the config atomically with owner-only permissions; it holds the
// token, so it must never become world-readable.
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked()
}

func (c *Config) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.s, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Update mutates the settings under lock and persists them.
func (c *Config) Update(fn func(*Settings)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(&c.s)
	return c.saveLocked()
}

// Snapshot returns a copy safe to read without holding the lock.
func (c *Config) Snapshot() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := c.s
	cp.LocalSignals = append([]LocalSignal(nil), c.s.LocalSignals...)
	return cp
}

func (c *Config) Path() string { return c.path }
