package com.relayproxy.android

import org.json.JSONObject

/**
 * Display names for Relay device exits and local custom proxy exits.
 * Routing and traffic data continue to carry stable IDs; only the UI uses
 * this inventory to resolve readable names.
 */
internal object ExitDisplayNames {
    fun fromStatus(status: JSONObject?, localNames: Map<String, String>): Map<String, String> {
        val names = localNames.toMutableMap()
        val exits = status?.optJSONArray("proxyExits")
        if (exits != null) {
            for (index in 0 until exits.length()) {
                val item = exits.optJSONObject(index) ?: continue
                val id = item.optString("deviceId").trim()
                val name = item.optString("name").trim().ifBlank {
                    item.optString("deviceName").trim()
                }
                if (id.isNotBlank() && name.isNotBlank()) names[id] = name
            }
        }
        return names
    }

    fun label(exitID: String, names: Map<String, String>): String {
        val id = exitID.trim()
        if (id.isEmpty()) return "跟随默认出口"
        val display = names[id].orEmpty().trim()
        return display.ifBlank { id }
    }
}
