package lua

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// DepotInfo holds depot-level metadata for Lua script generation.
type DepotInfo struct {
	DepotID    string
	ManifestID string
	DepotKey   string // hex-encoded, may be empty
}

// GameLua holds all information needed to generate an OpenSteamTool Lua script.
type GameLua struct {
	AppID  string
	Name   string
	Depots []DepotInfo
}

// GenerateLua produces a complete OpenSteamTool-compatible Lua script for the
// given game. The output registers app ownership, depot keys, and manifest
// bindings in the order expected by the tool runtime.
func GenerateLua(g *GameLua) string {
	var b strings.Builder

	// Header comment
	fmt.Fprintf(&b, "-- Game: %s (AppID: %s)\n", g.Name, g.AppID)

	// Main app registration — no depot key on the primary addappid call
	fmt.Fprintf(&b, "addappid(%s)\n", g.AppID)

	// Per-depot addappid with depot key (only when key is present)
	for _, d := range g.Depots {
		if d.DepotKey != "" {
			fmt.Fprintf(&b, "addappid(%s, \"%s\")\n", d.DepotID, d.DepotKey)
		}
	}

	// Manifest bindings — locks each depot to a specific manifest to prevent
	// unwanted updates from invalidating the unlock.
	for _, d := range g.Depots {
		if d.ManifestID != "" {
			fmt.Fprintf(&b, "setManifestid(%s, \"%s\")\n", d.DepotID, d.ManifestID)
		}
	}

	return b.String()
}

// GenerateStubLua produces a CDK-gated runtime-fetch Lua script. Instead of
// embedding the full unlock logic, the stub fetches the real payload from the
// server at runtime using the provided access token.
func GenerateStubLua(appID, serverURL, token string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "local r = http_get(\"%s/api/lua-payload?app=%s&t=%s\")\n", serverURL, appID, token)
	b.WriteString("if r then load(r)() end\n")

	return b.String()
}

// GenerateToken computes an HMAC-SHA256 token used to authenticate lua-payload
// fetch requests. The message is "appID|machine" keyed by the shared secret.
func GenerateToken(appID, machine, secret string) string {
	msg := appID + "|" + machine
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}
