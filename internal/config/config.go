package config

import (
	"encoding/json"
	"os"
	"sync"
)

// Config holds all server configuration.
type Config struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	DBPath     string `json:"db_path"`
	JWTSecret  string `json:"jwt_secret"`
	PublicURL  string `json:"public_url"`  // e.g. http://192.168.1.100:8787
	SteamPath  string `json:"steam_path"`  // server-side Steam install (optional)
	AdminUser  string `json:"admin_user"`
	AdminPass  string `json:"admin_pass"`
}

var (
	cfg  *Config
	once sync.Once
)

// DefaultConfig returns sane defaults.
func DefaultConfig() *Config {
	return &Config{
		Host:      "0.0.0.0",
		Port:      8787,
		DBPath:    "data/app.db",
		JWTSecret: "change-me-in-production",
		PublicURL: "",
		AdminUser: "admin",
		AdminPass: "admin123",
	}
}

// Load reads config.json or creates it with defaults.
func Load(path string) (*Config, error) {
	var c *Config
	data, err := os.ReadFile(path)
	if err != nil {
		c = DefaultConfig()
		if writeErr := Save(path, c); writeErr != nil {
			return nil, writeErr
		}
		return c, nil
	}
	c = DefaultConfig()
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	cfg = c
	return c, nil
}

// Save writes config to disk.
func Save(path string, c *Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Get returns the global config (must call Load first).
func Get() *Config {
	return cfg
}
