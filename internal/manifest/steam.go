package manifest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GameInfo holds basic info fetched from Steam store API.
type GameInfo struct {
	AppID    string
	Name     string
	Type     string // game, dlc, etc.
	HeaderURL string
}

// DepotKey holds a depot decryption key from ManifestHub.
type DepotKey struct {
	DepotID string
	Key     string
}

var (
	client = &http.Client{Timeout: 15 * time.Second}

	// In-memory cache for game info lookups
	nameCache   = map[string]*GameInfo{}
	nameCacheMu sync.RWMutex

	// Depot keys cache (loaded once from ManifestHub)
	depotKeys   map[string]string // depotID -> hex key
	depotKeysMu sync.RWMutex
	depotKeysLoaded bool
)

// LookupGame fetches game name + type from Steam store API.
func LookupGame(appID string) (*GameInfo, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, fmt.Errorf("appID is empty")
	}

	// Check cache
	nameCacheMu.RLock()
	if cached, ok := nameCache[appID]; ok {
		nameCacheMu.RUnlock()
		return cached, nil
	}
	nameCacheMu.RUnlock()

	url := fmt.Sprintf("https://store.steampowered.com/api/appdetails?appids=%s&l=schinese", appID)
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("Steam API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}

	// Response format: {"730": {"success": true, "data": {"name": "...", "type": "game", ...}}}
	var result map[string]struct {
		Success bool `json:"success"`
		Data    struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			HeaderImg string `json:"header_image"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	entry, ok := result[appID]
	if !ok || !entry.Success {
		return nil, fmt.Errorf("game not found: AppID %s", appID)
	}

	info := &GameInfo{
		AppID:     appID,
		Name:      entry.Data.Name,
		Type:      entry.Data.Type,
		HeaderURL: entry.Data.HeaderImg,
	}

	// Cache it
	nameCacheMu.Lock()
	nameCache[appID] = info
	nameCacheMu.Unlock()

	return info, nil
}

// ManifestHub depot keys JSON structure:
// { "depotID": "hexkey", ... }
// URL: https://raw.githubusercontent.com/SteamAutoCracks/ManifestHub/main/depotkeys.json

// LoadDepotKeys fetches depot keys from ManifestHub (cached).
func LoadDepotKeys() error {
	depotKeysMu.Lock()
	defer depotKeysMu.Unlock()

	if depotKeysLoaded {
		return nil
	}

	url := "https://raw.githubusercontent.com/SteamAutoCracks/ManifestHub/main/depotkeys.json"
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("fetch depot keys failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read depot keys failed: %w", err)
	}

	keys := map[string]string{}
	if err := json.Unmarshal(body, &keys); err != nil {
		return fmt.Errorf("parse depot keys failed: %w", err)
	}

	depotKeys = keys
	depotKeysLoaded = true
	return nil
}

// GetDepotKey returns the decryption key for a depot ID, or "" if not found.
func GetDepotKey(depotID string) string {
	depotKeysMu.RLock()
	defer depotKeysMu.RUnlock()
	return depotKeys[depotID]
}

// DepotKeysCount returns how many depot keys are loaded.
func DepotKeysCount() int {
	depotKeysMu.RLock()
	defer depotKeysMu.RUnlock()
	return len(depotKeys)
}

// FindDepotsForApp tries to find depot keys that likely belong to an app.
// Steam depot IDs for an app are typically appID*10+1, appID*10+2, etc.
// But the most common pattern is: depot = appID+1 for the main content depot.
// We also check appID itself and a range around it.
func FindDepotsForApp(appID string) []DepotKey {
	depotKeysMu.RLock()
	defer depotKeysMu.RUnlock()

	var result []DepotKey

	// Check common depot ID patterns
	// For most games: main depot = appID+1, sometimes appID itself
	candidates := []string{appID}

	// Parse appID as int to generate candidates
	var id int
	if _, err := fmt.Sscanf(appID, "%d", &id); err == nil {
		for delta := 0; delta <= 10; delta++ {
			candidates = append(candidates, fmt.Sprintf("%d", id+delta))
		}
	}

	seen := map[string]bool{}
	for _, depotID := range candidates {
		if seen[depotID] {
			continue
		}
		seen[depotID] = true
		if key, ok := depotKeys[depotID]; ok {
			result = append(result, DepotKey{DepotID: depotID, Key: key})
		}
	}

	return result
}
