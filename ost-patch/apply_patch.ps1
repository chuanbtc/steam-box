# apply_patch.ps1 — Apply CDK hook patch to OpenSteamTool source
param([string]$OstRoot = "ost")

$ErrorActionPreference = "Stop"

# 1. Copy new files
Copy-Item "ost-patch/Hooks_CDKRedeem.h"   "$OstRoot/src/Hook/" -Force
Copy-Item "ost-patch/Hooks_CDKRedeem.cpp"  "$OstRoot/src/Hook/" -Force
Write-Host "[+] Copied CDK hook files"

# 2. Patch CMakeLists.txt
$cmake = Get-Content "$OstRoot/src/CMakeLists.txt" -Raw
if ($cmake -notmatch "Hooks_CDKRedeem") {
    $cmake = $cmake.Replace("Hook/Hooks_NetPacket.cpp", "Hook/Hooks_NetPacket.cpp`n    Hook/Hooks_CDKRedeem.cpp")
    $cmake = $cmake.Replace("Bcrypt", "Bcrypt`n    Shell32")
    Set-Content "$OstRoot/src/CMakeLists.txt" -Value $cmake -NoNewline
    Write-Host "[+] Patched CMakeLists.txt"
}

# 3. Patch Hooks_NetPacket.cpp — add include
$npFile = "$OstRoot/src/Hook/Hooks_NetPacket.cpp"
$np = Get-Content $npFile -Raw
if ($np -notmatch "Hooks_CDKRedeem") {
    $np = $np.Replace('#include "Hooks_NetPacket.h"', "#include `"Hooks_NetPacket.h`"`n#include `"Hooks_CDKRedeem.h`"")

    # 4. Add case k_EMsgClientRegisterKey in SendJob's switch
    # Strategy: find "default:" + "return;" pattern in SendJob and insert before it
    $cdkBlock = @"

        case k_EMsgClientRegisterKey: {              // 743
            bool blocked = Hooks_CDKRedeem::HandleSend(pBody, cbBody);
            if (blocked) {
                g_NeedReplaceSend = true;
                g_cbSendNewBody = 0;
            }
            return;
        }

        default:
"@
    # Replace the first occurrence of standalone "default:\n            return;" in the send section
    # We search after "void SendJob" to avoid matching RecvJob's default
    $sendJobPos = $np.IndexOf("void SendJob")
    if ($sendJobPos -gt 0) {
        $afterSendJob = $np.Substring($sendJobPos)
        $defaultPattern = "        default:`r`n            return;"
        $altPattern = "        default:`n            return;"
        $pattern = if ($afterSendJob.Contains($defaultPattern)) { $defaultPattern } else { $altPattern }
        $idx = $afterSendJob.IndexOf($pattern)
        if ($idx -gt 0) {
            $before = $np.Substring(0, $sendJobPos + $idx)
            $after = $np.Substring($sendJobPos + $idx + $pattern.Length)
            $replacement = $cdkBlock.TrimStart()
            $replacement += "`n            return;"
            $np = $before + $replacement + $after
            Write-Host "[+] Injected CDK case into SendJob switch"
        } else {
            Write-Host "[!] WARNING: Could not find 'default: return;' in SendJob — manual patch needed"
        }
    }

    Set-Content $npFile -Value $np -NoNewline
    Write-Host "[+] Patched Hooks_NetPacket.cpp"
}

# 5. Register CDK log module in ost_log_modules.h
$modsFile = "$OstRoot/src/Utils/ost_log_modules.h"
$modsContent = Get-Content $modsFile -Raw
if ($modsContent -notmatch "CDK") {
    $modsContent = $modsContent.TrimEnd() + "`nOST_MOD(CDK,            `"cdk`")`n"
    Set-Content $modsFile -Value $modsContent -NoNewline
    Write-Host "[+] Registered CDK log module"
}

Write-Host "[OK] All patches applied"
