package com.relayproxy.android

import org.json.JSONObject

/** Read server presence explicitly. A cached inventory is not proof of liveness. */
internal data class ExitNodeState(val connected: Boolean, val authorized: Boolean, val online: Set<String>) {
    fun canProbe(id: String): Boolean = connected && authorized && id in online
    fun presence(id: String): String = when {
        !connected -> "状态未知"
        id !in online -> "离线"
        else -> "在线"
    }

    companion object {
        fun from(status: JSONObject?, clientEnabled: Boolean = true): ExitNodeState {
            val online = mutableSetOf<String>()
            status?.optJSONArray("proxyExits")?.let { exits ->
                for (i in 0 until exits.length()) {
                    val item = exits.optJSONObject(i) ?: continue
                    val id = item.optString("deviceId").trim()
                    if (id.isNotBlank() && item.optBoolean("online", false)) online += id
                }
            }
            return ExitNodeState(
                status?.optString("connectionState") == "CONNECTED",
                clientEnabled && status?.optBoolean("clientApproved", false) == true,
                online,
            )
        }
    }
}

internal data class ExitLatency(val latencyMs: Long, val path: String, val network: String = "unknown") {
    companion object {
        fun parse(raw: String, expectedID: String): ExitLatency {
            val result = JSONObject(raw)
            require(result.getString("exitId") == expectedID) { "出口检测响应不匹配" }
            val latency = result.getLong("latencyMs")
            require(latency > 0) { "出口延迟无效" }
            val path = result.getString("path")
            require(path in setOf("p2p_quic", "public_direct_quic", "relay_quic", "relay_tls")) { "出口路径无效" }
            return ExitLatency(latency, path, result.optJSONObject("pathInfo")?.optString("network", "unknown") ?: "unknown")
        }

        fun pathLabel(path: String, network: String = "unknown"): String = when (path) {
            "p2p_quic" -> if (network == "lan") "局域网直连 QUIC" else "P2P QUIC"
            "public_direct_quic" -> "公网直连 QUIC"
            "relay_quic" -> "中继 QUIC"
            "relay_tls" -> "中继 TLS"
            else -> "未检测路径"
        }
    }
}
