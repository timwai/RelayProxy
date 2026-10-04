package com.relayproxy.android

/** Bounded SOCKS5 identity encoding. Never send a partial shared-UID group. */
internal object FlowOwnerIdentity {
    const val UNKNOWN = "__android_unknown__"

    fun encode(packages: Collection<String>): String {
        val group = packages.map(String::trim).filter(String::isNotEmpty).distinct().sorted()
        if (group.isEmpty() || group.size > 16 || group.any { !validPackage(it) }) return UNKNOWN
        return group.joinToString("|").takeIf { it.length <= 240 } ?: UNKNOWN
    }

    /**
     * A VPN limited to one Android UID can safely retain that identity when
     * ConnectivityManager misses a short-lived or native socket tuple.
     */
    fun encodeSingleUidScope(packagesByUid: Map<Int, Collection<String>>): String {
        if (packagesByUid.size != 1) return UNKNOWN
        return encode(packagesByUid.values.single())
    }

    private fun validPackage(value: String): Boolean = value.length <= 200 &&
        value.split('.').all { segment ->
            segment.isNotEmpty() && segment.first() in ('a'..'z') + ('A'..'Z') &&
                segment.all { it in 'a'..'z' || it in 'A'..'Z' || it in '0'..'9' || it == '_' }
        }
}
