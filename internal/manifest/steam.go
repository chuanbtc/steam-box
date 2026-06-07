package manifest

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GameInfo holds basic info fetched from Steam store API.
type GameInfo struct {
	AppID     string
	Name      string
	Type      string // game, dlc, etc.
	HeaderURL string
	DLCs      []string // list of DLC AppIDs
}

// DepotKey holds a depot decryption key.
type DepotKey struct {
	DepotID string
	Key     string
}

// ManifestInfo holds manifest request code for a depot.
type ManifestInfo struct {
	DepotID    string
	ManifestID string
}

var (
	client = &http.Client{Timeout: 20 * time.Second}

	// In-memory cache for game info lookups
	nameCache   = map[string]*GameInfo{}
	nameCacheMu sync.RWMutex

	// Depot keys cache (aggregated from multiple sources)
	depotKeys       map[string]string // depotID -> hex key
	depotKeysMu     sync.RWMutex
	depotKeysLoaded bool

	// Manifest codes cache
	manifestCache   = map[string]string{} // depotID -> manifestID
	manifestCacheMu sync.RWMutex
)

// ─── Depot Key Sources ──────────────────────────────────────────────────────

// depotKeySource defines one upstream depot key repository.
type depotKeySource struct {
	Name string
	URL  string
	// Parser transforms raw response body into depotID->key map.
	// If nil, assumes standard JSON {"depotID": "hexKey", ...} format.
	Parser func(body []byte) (map[string]string, error)
}

var depotKeySources = []depotKeySource{
	{
		Name: "ManifestHub",
		URL:  "https://raw.githubusercontent.com/SteamAutoCracks/ManifestHub/main/depotkeys.json",
	},
	{
		Name: "Starter01-ManifestAutoUpdate",
		URL:  "https://raw.githubusercontent.com/Starter01/ManifestAutoUpdate/main/Key.json",
	},
	{
		Name: "ikun0014-ManifestHub",
		URL:  "https://raw.githubusercontent.com/ikun0014/ManifestHub/main/depotkeys.json",
	},
}

// ─── Public API ─────────────────────────────────────────────────────────────

// LookupGame fetches game name, type, and DLC list from Steam store API.
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

	var result map[string]struct {
		Success bool `json:"success"`
		Data    struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			HeaderImg string `json:"header_image"`
			DLC       []int  `json:"dlc"`
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

	// Collect DLC AppIDs
	for _, dlcID := range entry.Data.DLC {
		info.DLCs = append(info.DLCs, strconv.Itoa(dlcID))
	}

	// Cache it
	nameCacheMu.Lock()
	nameCache[appID] = info
	nameCacheMu.Unlock()

	return info, nil
}

// LoadDepotKeys fetches depot keys from ALL configured sources and merges them.
// Later sources overwrite earlier ones (last-write-wins).
func LoadDepotKeys() error {
	depotKeysMu.Lock()
	defer depotKeysMu.Unlock()

	if depotKeysLoaded {
		return nil
	}

	merged := map[string]string{}
	var totalNew int

	for _, src := range depotKeySources {
		keys, err := fetchDepotKeySource(src)
		if err != nil {
			log.Printf("[depot-keys] WARN: source %s failed: %v", src.Name, err)
			continue
		}
		added := 0
		for k, v := range keys {
			if _, exists := merged[k]; !exists {
				added++
			}
			merged[k] = v
		}
		totalNew += added
		log.Printf("[depot-keys] loaded %d keys from %s (%d new)", len(keys), src.Name, added)
	}

	if len(merged) == 0 {
		return fmt.Errorf("all depot key sources failed")
	}

	depotKeys = merged
	depotKeysLoaded = true
	log.Printf("[depot-keys] total: %d unique depot keys from %d sources", len(merged), len(depotKeySources))
	return nil
}

// ReloadDepotKeys forces a re-fetch of all depot key sources.
func ReloadDepotKeys() error {
	depotKeysMu.Lock()
	depotKeysLoaded = false
	depotKeysMu.Unlock()
	return LoadDepotKeys()
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

// FindDepotsForApp finds ALL depot keys for an app, including DLC depots.
// Uses Steam store API to discover the real depot IDs instead of guessing.
func FindDepotsForApp(appID string) []DepotKey {
	depotKeysMu.RLock()
	defer depotKeysMu.RUnlock()

	var result []DepotKey
	seen := map[string]bool{}

	// 1. Check the app itself and common depot patterns (appID+0 through appID+20)
	var id int
	if _, err := fmt.Sscanf(appID, "%d", &id); err == nil {
		for delta := 0; delta <= 20; delta++ {
			depotID := fmt.Sprintf("%d", id+delta)
			if seen[depotID] {
				continue
			}
			seen[depotID] = true
			if key, ok := depotKeys[depotID]; ok {
				result = append(result, DepotKey{DepotID: depotID, Key: key})
			}
		}
	}

	// 2. Also check DLC depots (each DLC has its own depot = dlcAppID+1 typically)
	info, err := LookupGame(appID)
	if err == nil && len(info.DLCs) > 0 {
		for _, dlcAppID := range info.DLCs {
			dlcID, _ := strconv.Atoi(dlcAppID)
			if dlcID == 0 {
				continue
			}
			// DLC depots are usually dlcAppID or dlcAppID+1
			for delta := 0; delta <= 1; delta++ {
				depotID := fmt.Sprintf("%d", dlcID+delta)
				if seen[depotID] {
					continue
				}
				seen[depotID] = true
				if key, ok := depotKeys[depotID]; ok {
					result = append(result, DepotKey{DepotID: depotID, Key: key})
				}
			}
		}
	}

	return result
}

// FindDLCsForApp returns the list of DLC AppIDs for a game.
func FindDLCsForApp(appID string) []string {
	info, err := LookupGame(appID)
	if err != nil {
		return nil
	}
	return info.DLCs
}

// ─── Manifest Code API ──────────────────────────────────────────────────────

// ManifestAPIProvider defines available manifest request code providers.
type ManifestAPIProvider string

const (
	ManifestProviderOST      ManifestAPIProvider = "opensteamtool"
	ManifestProviderWuDRM    ManifestAPIProvider = "wudrm"
	ManifestProviderSteamRun ManifestAPIProvider = "steamrun"
)

// FetchManifestCode retrieves a manifest request code for the given GID
// (globally unique manifest ID) from the configured upstream provider.
// The code is required by Steam CDN to authorize content downloads.
func FetchManifestCode(gid string, provider ManifestAPIProvider) (string, error) {
	// Check cache first
	manifestCacheMu.RLock()
	if code, ok := manifestCache[gid]; ok {
		manifestCacheMu.RUnlock()
		return code, nil
	}
	manifestCacheMu.RUnlock()

	var url string
	switch provider {
	case ManifestProviderWuDRM:
		url = fmt.Sprintf("http://gmrc.wudrm.com/manifest/%s", gid)
	case ManifestProviderSteamRun:
		url = fmt.Sprintf("https://manifest.steam.run/api/manifest/%s", gid)
	default:
		url = fmt.Sprintf("https://manifest.opensteamtool.com/%s", gid)
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("manifest request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read manifest response failed: %w", err)
	}

	code := strings.TrimSpace(string(body))

	// steamrun returns JSON: {"content":"123456789"}
	if provider == ManifestProviderSteamRun {
		var sr struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(body, &sr) == nil && sr.Content != "" {
			code = sr.Content
		}
	}

	if code == "" || len(code) > 25 {
		return "", fmt.Errorf("invalid manifest code from %s: %q", provider, code)
	}

	// Cache it
	manifestCacheMu.Lock()
	manifestCache[gid] = code
	manifestCacheMu.Unlock()

	return code, nil
}

// ─── Internal Helpers ───────────────────────────────────────────────────────

func fetchDepotKeySource(src depotKeySource) (map[string]string, error) {
	resp, err := client.Get(src.URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if src.Parser != nil {
		return src.Parser(body)
	}

	// Default: standard JSON map
	keys := map[string]string{}
	if err := json.Unmarshal(body, &keys); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return keys, nil
}
