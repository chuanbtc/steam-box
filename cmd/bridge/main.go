// cdk-bridge: Background CDK activation bridge for OpenSteamTool
//
// Runs as a system tray app. Two activation methods:
//   1. CLIPBOARD MONITOR: User copies a CDK (Ctrl+C) → auto-detected → auto-activated
//   2. TRAY MENU: Right-click tray icon → "激活CDK" → input dialog
//
// When a CDK is detected:
//   - Sends to server /api/redeem
//   - Writes lua to Steam/config/lua/ (OpenSteamTool hot-reloads it)
//   - Opens steam://install/{appid} to trigger download
//   - Shows a Windows toast notification
//
// No need to restart Steam. OpenSteamTool detects new lua files and reloads automatically.
//
// Build: GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-H windowsgui -s -w" -o cdk-bridge.exe ./cmd/bridge/

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type HookConfig struct {
	API    string `json:"api"`
	Steam  string `json:"steam"`
	Engine string `json:"engine"`
}

type RedeemResponse struct {
	OK      bool   `json:"ok"`
	AppID   string `json:"appid"`
	Name    string `json:"name"`
	LuaB64  string `json:"lua_b64"`
	Message string `json:"message"`
	Manifests []struct {
		Name string `json:"name"`
		B64  string `json:"b64"`
	} `json:"manifests"`
}

var (
	hookCfg       HookConfig
	steamPath     string
	cdkRegex      = regexp.MustCompile(`^[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}$`)
	processedCDKs sync.Map // prevent double-activation
	httpClient    = &http.Client{Timeout: 30 * time.Second}
)

func main() {
	log.SetFlags(log.Ltime)

	if err := loadConfig(); err != nil {
		log.Printf("配置加载失败: %v", err)
		showMessageBox("Steam Box CDK Bridge", fmt.Sprintf("配置加载失败: %v\n\n请先运行 hook 安装命令", err), true)
		return
	}

	log.Printf("服务器: %s | Steam: %s", hookCfg.API, steamPath)

	// Start local web server for manual activation UI
	go startWebUI()

	// Start clipboard monitor
	go clipboardMonitor()

	// Auto-open browser on first launch
	go func() {
		time.Sleep(800 * time.Millisecond)
		openBrowser("http://127.0.0.1:18787")
	}()

	// Keep running (tray icon would go here on full implementation)
	// For now, block on the web server
	select {}
}

func loadConfig() error {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
	}
	hookPath := filepath.Join(localAppData, "steam", "hook.json")

	data, err := os.ReadFile(hookPath)
	if err != nil {
		return fmt.Errorf("未找到 hook.json: %w\n请先运行 hook 安装命令", err)
	}
	if err := json.Unmarshal(data, &hookCfg); err != nil {
		return fmt.Errorf("hook.json 格式错误: %w", err)
	}
	if hookCfg.API == "" {
		return fmt.Errorf("hook.json 缺少 api 字段")
	}

	steamPath = strings.ReplaceAll(hookCfg.Steam, "/", string(os.PathSeparator))
	if steamPath == "" {
		for _, p := range []string{`C:\Program Files (x86)\Steam`, `D:\Steam`} {
			if _, err := os.Stat(filepath.Join(p, "steam.exe")); err == nil {
				steamPath = p
				break
			}
		}
	}
	if steamPath == "" {
		return fmt.Errorf("无法确定 Steam 路径")
	}
	return nil
}

// ── Clipboard Monitor ──────────────────────────────────────────────────────

func clipboardMonitor() {
	lastClip := ""
	for {
		time.Sleep(500 * time.Millisecond)
		text := getClipboardText()
		if text == lastClip || text == "" {
			continue
		}
		lastClip = text

		// Check if clipboard contains a CDK
		cleaned := strings.ToUpper(strings.TrimSpace(text))
		if cdkRegex.MatchString(cleaned) {
			// Don't process the same CDK twice
			if _, loaded := processedCDKs.LoadOrStore(cleaned, true); loaded {
				continue
			}
			log.Printf("剪贴板检测到 CDK: %s", cleaned)
			go activateCDK(cleaned, true)
		}
	}
}

func getClipboardText() string {
	// Use PowerShell to read clipboard (works on all Windows versions)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", "Get-Clipboard")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ── CDK Activation Core ────────────────────────────────────────────────────

func activateCDK(code string, fromClipboard bool) (*RedeemResponse, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !cdkRegex.MatchString(code) {
		return nil, fmt.Errorf("CDK 格式错误")
	}

	hostname, _ := os.Hostname()
	username := os.Getenv("USERNAME")
	machine := fmt.Sprintf("%s|%s", hostname, username)

	body, _ := json.Marshal(map[string]string{"cdk": code, "machine": machine})
	resp, err := httpClient.Post(hookCfg.API+"/api/redeem", "application/json", bytes.NewReader(body))
	if err != nil {
		msg := fmt.Sprintf("无法连接服务器: %v", err)
		if fromClipboard {
			showMessageBox("CDK 激活失败", msg, true)
		}
		return nil, fmt.Errorf(msg)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var redeem RedeemResponse
	if err := json.Unmarshal(respBody, &redeem); err != nil {
		msg := "服务器返回异常"
		if fromClipboard {
			showMessageBox("CDK 激活失败", msg, true)
		}
		return nil, fmt.Errorf(msg)
	}

	if !redeem.OK {
		msg := redeem.Message
		if msg == "" {
			msg = "激活失败"
		}
		if fromClipboard {
			showMessageBox("CDK 激活失败", msg, true)
		}
		return nil, fmt.Errorf(msg)
	}

	// Write lua to Steam/config/lua/
	luaDir := filepath.Join(steamPath, "config", "lua")
	os.MkdirAll(luaDir, 0755)

	if redeem.LuaB64 != "" {
		luaBytes, err := base64.StdEncoding.DecodeString(redeem.LuaB64)
		if err == nil {
			luaFile := filepath.Join(luaDir, fmt.Sprintf("game_%s.lua", redeem.AppID))
			os.WriteFile(luaFile, luaBytes, 0644)
			log.Printf("lua 已写入: %s", luaFile)
		}
	}

	// Write manifests
	if len(redeem.Manifests) > 0 {
		depotDir := filepath.Join(steamPath, "config", "depotcache")
		os.MkdirAll(depotDir, 0755)
		for _, m := range redeem.Manifests {
			mBytes, _ := base64.StdEncoding.DecodeString(m.B64)
			if len(mBytes) > 0 {
				os.WriteFile(filepath.Join(depotDir, m.Name), mBytes, 0644)
			}
		}
	}

	gameName := redeem.Name
	if gameName == "" {
		gameName = "AppID " + redeem.AppID
	}

	// Open steam://install to trigger download
	go func() {
		time.Sleep(500 * time.Millisecond)
		exec.Command("cmd", "/c", "start", fmt.Sprintf("steam://install/%s", redeem.AppID)).Run()
	}()

	if fromClipboard {
		showMessageBox("激活成功 ✓", fmt.Sprintf("游戏: %s\nAppID: %s\n\nSteam 正在准备安装...", gameName, redeem.AppID), false)
	}

	log.Printf("激活成功: %s (AppID: %s)", gameName, redeem.AppID)
	return &redeem, nil
}

func showMessageBox(title, text string, isError bool) {
	// Use PowerShell for MessageBox (works without CGO)
	icon := "Information"
	if isError {
		icon = "Error"
	}
	ps := fmt.Sprintf(
		`Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.MessageBox]::Show("%s", "%s", "OK", "%s")`,
		strings.ReplaceAll(strings.ReplaceAll(text, `"`, "`\""), "\n", "`n"),
		strings.ReplaceAll(title, `"`, "`\""),
		icon,
	)
	exec.Command("powershell", "-NoProfile", "-Command", ps).Run()
}

func openBrowser(url string) {
	exec.Command("cmd", "/c", "start", url).Run()
}

// ── Local Web UI ───────────────────────────────────────────────────────────

func startWebUI() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handlePage)
	mux.HandleFunc("/api/activate", handleWebActivate)
	mux.HandleFunc("/api/status", handleWebStatus)

	log.Printf("本地激活页面: http://127.0.0.1:18787")
	http.ListenAndServe("127.0.0.1:18787", mux)
}

func handleWebStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true, "server": hookCfg.API, "steam": steamPath,
	})
}

func handleWebActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct {
		CDK string `json:"cdk"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	processedCDKs.Store(strings.ToUpper(strings.TrimSpace(req.CDK)), true)
	result, err := activateCDK(req.CDK, false)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "message": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true, "appid": result.AppID, "name": result.Name,
	})
}

func handlePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, pageHTML)
}

const pageHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Steam 产品激活</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:"Microsoft YaHei","Segoe UI",sans-serif;background:linear-gradient(135deg,#1b2838,#171a21);color:#c7d5e0;min-height:100vh;display:flex;justify-content:center;align-items:center}
.box{background:#1b2838;border:1px solid #2a475e;border-radius:4px;width:540px;box-shadow:0 0 40px rgba(0,0,0,.5);overflow:hidden}
.hd{background:linear-gradient(90deg,#2a475e,#1b2838);padding:16px 24px;border-bottom:1px solid #2a475e;display:flex;align-items:center;gap:12px}
.hd .ico{width:32px;height:32px;background:#66c0f4;border-radius:6px;display:flex;align-items:center;justify-content:center;font-weight:bold;font-size:18px;color:#1b2838}
.hd h1{font-size:16px;font-weight:500;color:#fff}
.bd{padding:32px 24px}
.bd p{color:#8f98a0;font-size:13px;margin-bottom:20px;line-height:1.6}
.ig{margin-bottom:24px}
.ig label{display:block;font-size:12px;color:#8f98a0;text-transform:uppercase;letter-spacing:1px;margin-bottom:8px}
.ig input{width:100%;padding:12px 16px;background:#2a475e;border:1px solid #4a6b82;border-radius:3px;color:#fff;font-size:18px;font-family:Consolas,monospace;letter-spacing:3px;text-align:center;outline:none;transition:border-color .2s}
.ig input:focus{border-color:#66c0f4}
.ig input::placeholder{color:#4a6b82;letter-spacing:2px;font-size:14px}
.tips{background:rgba(102,192,244,.08);border:1px solid rgba(102,192,244,.2);border-radius:4px;padding:12px 16px;margin-bottom:24px;font-size:12px;color:#66c0f4;line-height:1.8}
.tips b{color:#fff}
.br{display:flex;gap:12px;justify-content:flex-end}
.btn{padding:10px 32px;border:none;border-radius:3px;font-size:14px;cursor:pointer;font-weight:500;transition:all .2s}
.bp{background:linear-gradient(90deg,#47bfff,#1a9fff);color:#fff}
.bp:hover{background:linear-gradient(90deg,#66c0f4,#47bfff)}
.bp:disabled{opacity:.5;cursor:not-allowed}
.bc{background:#2a475e;color:#8f98a0}.bc:hover{background:#3d6075}
.res{display:none;text-align:center;padding:20px 0}
.res.show{display:block}
.res-icon{font-size:48px;margin-bottom:12px}
.res-t{font-size:18px;color:#fff;margin-bottom:8px}
.res-s{font-size:13px;color:#8f98a0}
.res-g{margin-top:16px;padding:12px;background:rgba(102,192,244,.1);border:1px solid rgba(102,192,244,.3);border-radius:4px;font-size:15px;color:#66c0f4}
.err{color:#e55;font-size:13px;margin-top:8px;display:none}
.sp{display:inline-block;width:16px;height:16px;border:2px solid #fff;border-top-color:transparent;border-radius:50%;animation:spin .8s linear infinite;vertical-align:middle;margin-right:8px}
@keyframes spin{to{transform:rotate(360deg)}}
.ft{padding:12px 24px;border-top:1px solid #2a475e;text-align:center;font-size:11px;color:#4a6b82}
</style>
</head>
<body>
<div class="box">
<div class="hd"><div class="ico">S</div><h1>产品激活</h1></div>
<div class="bd">
<div id="fv">
<p>请输入您的产品激活码。</p>
<div class="tips">
  <b>💡 快捷方式：</b>直接复制 CDK 到剪贴板（Ctrl+C），程序会自动检测并激活！<br>
  或者在下方手动输入激活码。
</div>
<div class="ig">
<label>产品激活码</label>
<input type="text" id="ci" placeholder="XXXX-XXXX-XXXX-XXXX" maxlength="19" autocomplete="off" autofocus oninput="fmt(this)" onkeydown="if(event.key==='Enter')go()">
</div>
<div class="err" id="em"></div>
<div class="br">
<button class="btn bc" onclick="window.close()">取消</button>
<button class="btn bp" id="ab" onclick="go()">激活</button>
</div>
</div>
<div class="res" id="rok">
<div class="res-icon">✅</div>
<div class="res-t">激活成功！</div>
<div class="res-s">游戏已添加到您的 Steam 库，正在准备安装...</div>
<div class="res-g" id="rg"></div>
<div style="margin-top:20px"><button class="btn bp" onclick="reset()">继续激活</button></div>
</div>
<div class="res" id="rfail">
<div class="res-icon">❌</div>
<div class="res-t">激活失败</div>
<div class="res-s" id="fr"></div>
<div style="margin-top:20px"><button class="btn bp" onclick="reset()">重试</button></div>
</div>
</div>
<div class="ft">Steam Box · 复制CDK自动激活 · OpenSteamTool</div>
</div>
<script>
function fmt(e){let v=e.value.replace(/[^A-Za-z0-9]/g,'').toUpperCase(),p=[];for(let i=0;i<v.length&&i<16;i+=4)p.push(v.substring(i,i+4));e.value=p.join('-')}
async function go(){
const i=document.getElementById('ci'),b=document.getElementById('ab'),er=document.getElementById('em'),c=i.value.trim().toUpperCase();
er.style.display='none';
if(!/^[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}$/.test(c)){er.textContent='请输入正确格式的激活码';er.style.display='block';i.focus();return}
b.disabled=true;b.innerHTML='<span class="sp"></span>激活中...';
try{const r=await fetch('/api/activate',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({cdk:c})});const d=await r.json();
if(d.ok){document.getElementById('fv').style.display='none';document.getElementById('rok').classList.add('show');document.getElementById('rg').textContent=(d.name||'AppID '+d.appid)+' (AppID: '+d.appid+')'}
else{document.getElementById('fv').style.display='none';document.getElementById('rfail').classList.add('show');document.getElementById('fr').textContent=d.message||'未知错误'}}
catch(e){er.textContent='网络错误: '+e.message;er.style.display='block'}
finally{b.disabled=false;b.innerHTML='激活'}}
function reset(){document.getElementById('fv').style.display='block';document.getElementById('rok').classList.remove('show');document.getElementById('rfail').classList.remove('show');document.getElementById('ci').value='';document.getElementById('ci').focus()}
window.onload=()=>document.getElementById('ci').focus()
</script>
</body>
</html>`
