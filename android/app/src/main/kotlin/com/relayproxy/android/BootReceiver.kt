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
        val vpnCanStart = vpnDesired && VpnService.prepare(context) == null
        if (vpnDesired && !vpnCanStart) store.setVpnDesiredRunning(false)

        // Saved credentials alone never justify reconnecting after reboot or
        // app replacement. Only an enabled exit, VPN, or local proxy can.
        if (!store.shouldRunCore(config)) return
        val service = Intent(context, RelayExitService::class.java)
            .setAction(if (relayDesired) RelayExitService.ACTION_START else RelayExitService.ACTION_RECONFIGURE)
        startService(context, service)
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
