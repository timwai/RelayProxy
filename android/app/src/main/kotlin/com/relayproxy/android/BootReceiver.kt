package com.relayproxy.android

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.Build

class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        if (intent?.action != Intent.ACTION_BOOT_COMPLETED &&
            intent?.action != Intent.ACTION_MY_PACKAGE_REPLACED
        ) {
            return
        }

        val store = ConfigStore(context)
        val relayDesired = store.isDesiredRunning()
        val vpnDesired = store.isVpnDesiredRunning()
        val config = store.load()
        val localProxyDesired = config.clientEnabled
        val controlDesired = store.hasConnectionConfig(config)
        val vpnCanStart = vpnDesired && VpnService.prepare(context) == null
        if (vpnDesired && !vpnCanStart) store.setVpnDesiredRunning(false)
        if (!relayDesired && !vpnCanStart && !localProxyDesired && !controlDesired) {
            return
        }

        if (relayDesired || vpnCanStart || localProxyDesired || controlDesired) {
            val service = Intent(context, RelayExitService::class.java)
                .setAction(
                    if (relayDesired) {
                        RelayExitService.ACTION_START
                    } else if (vpnCanStart || localProxyDesired) {
                        RelayExitService.ACTION_RECONFIGURE
                    } else {
                        RelayExitService.ACTION_CONNECT
                    }
                )
            startService(context, service)
        }
        if (vpnCanStart) {
            startService(
                context,
                Intent(context, RelayVpnService::class.java)
                    .setAction(RelayVpnService.ACTION_START),
            )
        }
    }

    private fun startService(context: Context, service: Intent) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            context.startForegroundService(service)
        } else {
            context.startService(service)
        }
    }
}
