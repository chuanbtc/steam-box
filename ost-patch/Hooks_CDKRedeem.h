#pragma once

#include <cstdint>

// CDK Redeem: intercept Steam's "Activate a Product" (k_EMsgClientRegisterKey = 743)
// and redirect to a custom server for CDK validation.
namespace Hooks_CDKRedeem {
    // Called from SendJob when eMsg == k_EMsgClientRegisterKey.
    // Returns true if the message should be BLOCKED (not sent to Valve).
    bool HandleSend(const uint8_t* pBody, uint32_t cbBody);

    // Called from RecvJob when eMsg == 744 (RegisterKeyResponse).
    // Only used if we need to inject a fake success response.
    // (In practice we block the send, so no response comes from Valve.)
    void HandleRecv(const uint8_t* pBody, uint32_t cbBody,
                    const uint8_t* pHdr, uint32_t cbHdr);
}
