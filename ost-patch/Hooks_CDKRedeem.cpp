// Hooks_CDKRedeem.cpp — Intercept Steam's "Activate a Product" CDK flow
//
// When the user enters a CDK in Steam's "Activate a Product" dialog:
//   1. Steam sends k_EMsgClientRegisterKey (743) with the CDK string
//   2. We intercept it HERE before it reaches Valve
//   3. We POST the CDK to our server's /api/redeem
//   4. If the server says OK, we write the lua to config/lua/
//   5. We trigger a license refresh so the game appears immediately
//   6. We BLOCK the original message (return true) so Valve never sees it
//
// The server URL is read from hook.json in %LOCALAPPDATA%\steam\.

#include "Hooks_CDKRedeem.h"
#include "Hooks_Package.h"
#include "dllmain.h"
#include "Utils/WinHttp.h"
#include "Utils/Log.h"
#include "Utils/LuaConfig.h"
#include "Utils/Config.h"

#include "steam_messages.pb.h"

#include <fstream>
#include <filesystem>
#include <string>
#include <sstream>
#include <windows.h>
#include <shlobj.h>

namespace fs = std::filesystem;

LOG_MODULE(CDK);

namespace {

    // ── Read server URL from hook.json ─────────────────────────────
    std::string g_cdkServerUrl;
    bool        g_configLoaded = false;

    std::string ReadHookJson() {
        // Get %LOCALAPPDATA%
        char localAppData[MAX_PATH] = {};
        if (SUCCEEDED(SHGetFolderPathA(nullptr, CSIDL_LOCAL_APPDATA, nullptr, 0, localAppData))) {
            std::string hookPath = std::string(localAppData) + "\\steam\\hook.json";
            std::ifstream file(hookPath);
            if (file.is_open()) {
                std::stringstream ss;
                ss << file.rdbuf();
                return ss.str();
            }
        }
        return "";
    }

    std::string ExtractJsonString(const std::string& json, const std::string& key) {
        // Simple JSON string extractor (no dependency on a JSON lib)
        std::string needle = "\"" + key + "\"";
        size_t pos = json.find(needle);
        if (pos == std::string::npos) return "";
        pos = json.find('"', pos + needle.size());
        if (pos == std::string::npos) return "";
        pos++; // skip opening quote
        size_t end = json.find('"', pos);
        if (end == std::string::npos) return "";
        return json.substr(pos, end - pos);
    }

    void EnsureConfig() {
        if (g_configLoaded) return;
        g_configLoaded = true;

        std::string hookJson = ReadHookJson();
        if (hookJson.empty()) {
            LOG_CDK_WARN("hook.json not found — CDK redeem disabled");
            return;
        }
        g_cdkServerUrl = ExtractJsonString(hookJson, "api");
        if (g_cdkServerUrl.empty()) {
            LOG_CDK_WARN("hook.json has no 'api' field — CDK redeem disabled");
        } else {
            // Remove trailing slash
            while (!g_cdkServerUrl.empty() && g_cdkServerUrl.back() == '/')
                g_cdkServerUrl.pop_back();
            LOG_CDK_INFO("CDK server: {}", g_cdkServerUrl);
        }
    }

    // ── Extract CDK string from CMsgClientRegisterKey ──────────────
    // The proto message is simple: field 1 = string (the activation code)
    // We parse it manually since we might not have a generated class for it.
    std::string ExtractCDKFromBody(const uint8_t* pBody, uint32_t cbBody) {
        // Protobuf: field 1, wire type 2 (length-delimited) = tag byte 0x0A
        // Format: 0x0A <varint length> <bytes>
        if (cbBody < 3 || pBody[0] != 0x0A) return "";

        uint32_t len = 0;
        uint32_t offset = 1;
        // Decode varint
        for (int i = 0; i < 4 && offset < cbBody; i++) {
            uint8_t b = pBody[offset++];
            len |= (b & 0x7F) << (7 * i);
            if (!(b & 0x80)) break;
        }

        if (len == 0 || offset + len > cbBody) return "";
        return std::string(reinterpret_cast<const char*>(pBody + offset), len);
    }

    // ── Extract field from JSON response ──────────────────────────
    std::string ExtractBase64Lua(const std::string& json) {
        return ExtractJsonString(json, "lua_b64");
    }

    bool ExtractOkField(const std::string& json) {
        // Look for "ok":true
        return json.find("\"ok\":true") != std::string::npos ||
               json.find("\"ok\": true") != std::string::npos;
    }

    // ── Base64 decode ─────────────────────────────────────────────
    static const std::string b64chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

    std::string Base64Decode(const std::string& encoded) {
        std::string decoded;
        int val = 0, bits = -8;
        for (char c : encoded) {
            if (c == '=' || c == '\n' || c == '\r') continue;
            size_t pos = b64chars.find(c);
            if (pos == std::string::npos) continue;
            val = (val << 6) | static_cast<int>(pos);
            bits += 6;
            if (bits >= 0) {
                decoded.push_back(static_cast<char>((val >> bits) & 0xFF));
                bits -= 8;
            }
        }
        return decoded;
    }

    // ── Get Steam path from config or registry ────────────────────
    std::string GetSteamPath() {
        // Try hook.json first
        std::string hookJson = ReadHookJson();
        std::string steamPath = ExtractJsonString(hookJson, "steam");
        if (!steamPath.empty()) {
            // Normalize slashes
            for (auto& c : steamPath) if (c == '/') c = '\\';
            if (fs::exists(steamPath)) return steamPath;
        }

        // Fall back to registry
        HKEY hKey;
        if (RegOpenKeyExA(HKEY_CURRENT_USER, "Software\\Valve\\Steam", 0, KEY_READ, &hKey) == ERROR_SUCCESS) {
            char buf[MAX_PATH] = {};
            DWORD size = sizeof(buf);
            if (RegQueryValueExA(hKey, "SteamPath", nullptr, nullptr, (LPBYTE)buf, &size) == ERROR_SUCCESS) {
                steamPath = std::string(buf, size - 1);
                for (auto& c : steamPath) if (c == '/') c = '\\';
            }
            RegCloseKey(hKey);
        }
        return steamPath;
    }

    // ── Show a toast/message to the user ──────────────────────────
    void ShowResult(const std::string& title, const std::string& msg, bool isError) {
        UINT flags = MB_OK | MB_TOPMOST | MB_SETFOREGROUND;
        flags |= isError ? MB_ICONERROR : MB_ICONINFORMATION;
        // Run in a new thread so we don't block the network thread
        std::string t = title, m = msg;
        std::thread([t, m, flags]() {
            MessageBoxA(nullptr, m.c_str(), t.c_str(), flags);
        }).detach();
    }

} // anonymous namespace


namespace Hooks_CDKRedeem {

    bool HandleSend(const uint8_t* pBody, uint32_t cbBody) {
        EnsureConfig();

        if (g_cdkServerUrl.empty()) {
            LOG_CDK_DEBUG("No CDK server configured, passing through to Valve");
            return false; // Don't block — let Valve handle it
        }

        // Extract the CDK string from the protobuf message
        std::string cdk = ExtractCDKFromBody(pBody, cbBody);
        if (cdk.empty()) {
            LOG_CDK_WARN("Failed to extract CDK from RegisterKey message");
            return false;
        }

        LOG_CDK_INFO("Intercepted CDK: {}", cdk);

        // Build machine identifier
        char computerName[MAX_COMPUTERNAME_LENGTH + 1] = {};
        DWORD compSize = sizeof(computerName);
        GetComputerNameA(computerName, &compSize);

        char userName[256] = {};
        DWORD userSize = sizeof(userName);
        GetUserNameA(userName, &userSize);

        std::string machine = std::string(computerName) + "|" + std::string(userName);

        // POST to server /api/redeem
        std::string url = g_cdkServerUrl + "/api/redeem";
        std::string body = "{\"cdk\":\"" + cdk + "\",\"machine\":\"" + machine + "\"}";

        LOG_CDK_INFO("POST {} body={}", url, body);

        WinHttp::Result result = WinHttp::Execute(
            L"POST", url.c_str(),
            body.c_str(), static_cast<DWORD>(body.size()),
            L"Content-Type: application/json\r\n",
            5000, 5000, 30000, 30000
        );

        if (!result.ok || result.status != 200) {
            LOG_CDK_WARN("Server request failed: status={} ok={}", result.status, result.ok);
            // Don't block — fall through to Valve (will show "invalid key" but at least doesn't silently fail)
            return false;
        }

        LOG_CDK_INFO("Server response: {}", result.body);

        if (!ExtractOkField(result.body)) {
            // Server said CDK is invalid — extract error message
            std::string errMsg = ExtractJsonString(result.body, "message");
            if (errMsg.empty()) errMsg = "CDK 无效";
            LOG_CDK_INFO("CDK rejected by server: {}", errMsg);
            ShowResult("CDK 激活失败", errMsg, true);
            return true; // Block the message — don't send invalid CDK to Valve either
        }

        // ── Success! Write lua and trigger reload ──────────────────

        std::string appid = ExtractJsonString(result.body, "appid");
        std::string gameName = ExtractJsonString(result.body, "name");
        std::string luaB64 = ExtractBase64Lua(result.body);

        if (luaB64.empty() || appid.empty()) {
            LOG_CDK_WARN("Server response missing lua_b64 or appid");
            ShowResult("CDK 激活失败", "服务器返回数据不完整", true);
            return true;
        }

        // Decode base64 lua
        std::string luaContent = Base64Decode(luaB64);
        if (luaContent.empty()) {
            LOG_CDK_WARN("Failed to decode lua_b64");
            ShowResult("CDK 激活失败", "Lua 解码失败", true);
            return true;
        }

        // Write to Steam/config/lua/game_{appid}.lua
        std::string steamPath = GetSteamPath();
        if (steamPath.empty()) {
            LOG_CDK_WARN("Cannot determine Steam path for lua write");
            ShowResult("CDK 激活失败", "无法确定 Steam 路径", true);
            return true;
        }

        fs::path luaDir = fs::path(steamPath) / "config" / "lua";
        fs::create_directories(luaDir);

        fs::path luaFile = luaDir / ("game_" + appid + ".lua");
        {
            std::ofstream ofs(luaFile, std::ios::binary);
            ofs.write(luaContent.data(), luaContent.size());
        }
        LOG_CDK_INFO("Wrote lua: {}", luaFile.string());

        // OpenSteamTool's FileWatcher will detect the new file and hot-reload it.
        // After reload, Hooks_Package::NotifyLicenseChanged() gets called,
        // which adds the new AppIDs to the fake license and refreshes the library.

        // Show success to user
        std::string successMsg = gameName.empty()
            ? ("AppID " + appid + " 已激活")
            : (gameName + " (AppID: " + appid + ") 已激活\n\nSteam 库将自动刷新，请稍候...");
        ShowResult("激活成功", successMsg, false);

        LOG_CDK_INFO("CDK activation complete: {} ({})", gameName, appid);

        // BLOCK the original RegisterKey message — don't let it go to Valve
        return true;
    }

    void HandleRecv(const uint8_t* pBody, uint32_t cbBody,
                    const uint8_t* pHdr, uint32_t cbHdr) {
        // If we blocked the send, Valve won't send a response.
        // This handler is here for future use (e.g., if we want to
        // pass through to Valve and only intercept the failure response).
        LOG_CDK_DEBUG("RegisterKeyResponse received (unexpected if we blocked send)");
    }

}
