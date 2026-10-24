package com.follow.clash.service.modules

import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkCapabilities.TRANSPORT_SATELLITE
import android.net.NetworkCapabilities.TRANSPORT_USB
import android.net.NetworkRequest
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.Looper
import android.os.PowerManager
import androidx.core.content.ContextCompat
import androidx.core.content.getSystemService
import com.follow.clash.common.BroadcastAction
import com.follow.clash.common.sendBroadcast
import com.follow.clash.core.Core
import java.net.Inet4Address
import java.net.Inet6Address
import java.net.InetAddress
import java.util.concurrent.ConcurrentHashMap

private data class NetworkInfo(
    @Volatile var losingUntilMillis: Long = 0,
    @Volatile var dnsList: List<InetAddress> = emptyList(),
) {
    val priorityPenalty: Int
        get() = if (losingUntilMillis > System.currentTimeMillis()) 10 else 0
}

internal class NetworkObserveModule(private val service: Service) : ServiceModule {

    private val networkInfos = ConcurrentHashMap<Network, NetworkInfo>()
    private val connectivity by lazy {
        service.getSystemService<ConnectivityManager>()
    }
    private val handler = Handler(networkObserveLooper)
    private var currentDnsList = listOf<String>()
    private var currentNetwork: Network? = null
    private var currentNetworkKey = ""
    @Volatile
    private var active = false
    private var receiverRegistered = false
    private var callbackRegistered = false

    private val wakeCheckTask = Runnable {
        synchronized(this) {
            if (active) {
                refreshNetworks()
                updateDns()
                Core.smartRecheck()
            }
        }
    }

    private val recoveryTask = object : Runnable {
        override fun run() {
            if (!active) return
            if (Core.takeSmartRestartRequest()) {
                BroadcastAction.VPN_RECOVERY_REQUESTED.sendBroadcast()
            }
            if (active) handler.postDelayed(this, RECOVERY_CHECK_INTERVAL_MILLIS)
        }
    }

    private val wakeReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            if (!active) return
            if (intent?.action == PowerManager.ACTION_DEVICE_IDLE_MODE_CHANGED &&
                service.getSystemService<PowerManager>()?.isDeviceIdleMode == true
            ) return
            handler.removeCallbacks(wakeCheckTask)
            handler.postDelayed(wakeCheckTask, WAKE_CHECK_DELAY_MILLIS)
        }
    }

    private val request = NetworkRequest.Builder().apply {
        addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
        addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            addCapability(NetworkCapabilities.NET_CAPABILITY_FOREGROUND)
        }
        addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_RESTRICTED)
    }.build()

    private val callback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            networkInfos[network] = NetworkInfo()
            scheduleUpdateDns()
        }

        override fun onLosing(network: Network, maxMsToLive: Int) {
            val info = networkInfos[network] ?: return
            info.losingUntilMillis = System.currentTimeMillis() + maxMsToLive
            scheduleUpdateDns()
            if (maxMsToLive > 0) {
                handler.postDelayed({
                    if (networkInfos.containsKey(network)) {
                        updateDns()
                    }
                }, maxMsToLive.toLong() + 50)
            }
        }

        override fun onLost(network: Network) {
            networkInfos.remove(network)
            scheduleUpdateDns()
        }

        override fun onLinkPropertiesChanged(network: Network, linkProperties: LinkProperties) {
            networkInfos[network]?.dnsList = linkProperties.dnsServers
            scheduleUpdateDns()
        }

        override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) {
            scheduleUpdateDns()
        }
    }

    private val updateDnsTask = Runnable { updateDns() }

    // A switch arrives as a burst, and a network that just came up has no DNS
    // until its link properties follow; the Core flushes its cache per list.
    private fun scheduleUpdateDns() {
        if (!active) return
        handler.removeCallbacks(updateDnsTask)
        handler.postDelayed(updateDnsTask, DNS_UPDATE_DELAY_MILLIS)
    }

    override fun start() {
        active = true
        try {
            updateDns()
            connectivity?.registerNetworkCallback(request, callback)
            callbackRegistered = connectivity != null
            ContextCompat.registerReceiver(
                service,
                wakeReceiver,
                IntentFilter().apply {
                    addAction(Intent.ACTION_SCREEN_ON)
                    addAction(Intent.ACTION_USER_PRESENT)
                    addAction(PowerManager.ACTION_DEVICE_IDLE_MODE_CHANGED)
                },
                ContextCompat.RECEIVER_EXPORTED,
            )
            receiverRegistered = true
            handler.postDelayed(recoveryTask, RECOVERY_CHECK_INTERVAL_MILLIS)
        } catch (error: Throwable) {
            stop()
            throw error
        }
    }

    @Suppress("DEPRECATION")
    private fun refreshNetworks() {
        val available = connectivity?.allNetworks.orEmpty().filter { network ->
            val capabilities = connectivity?.getNetworkCapabilities(network)
            capabilities?.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) == true &&
                capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN) &&
                capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_RESTRICTED) &&
                (Build.VERSION.SDK_INT < Build.VERSION_CODES.P ||
                    capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_FOREGROUND))
        }.toSet()
        networkInfos.keys.retainAll(available)
        available.forEach { network ->
            networkInfos.getOrPut(network) { NetworkInfo() }.dnsList =
                connectivity?.getLinkProperties(network)?.dnsServers.orEmpty()
        }
    }

    private fun networkPriority(entry: Map.Entry<Network, NetworkInfo>): Int {
        val capabilities = connectivity?.getNetworkCapabilities(entry.key)
        return when {
            capabilities == null -> 100
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_VPN) -> 90
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> 0
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> 1
            Build.VERSION.SDK_INT >= Build.VERSION_CODES.S &&
                capabilities.hasTransport(TRANSPORT_USB) -> 2

            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_BLUETOOTH) -> 3
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> 4
            Build.VERSION.SDK_INT >= Build.VERSION_CODES.VANILLA_ICE_CREAM &&
                capabilities.hasTransport(TRANSPORT_SATELLITE) -> 5

            else -> 20
        } + entry.value.priorityPenalty
    }

    @Synchronized
    private fun updateDns(force: Boolean = false) {
        if (!active && !force) return
        val preferred = networkInfos.entries.minByOrNull(::networkPriority)
        val dnsList = preferred
            ?.value
            ?.dnsList
            .orEmpty()
            .map { address -> address.asSocketAddressText(DNS_PORT) }
            .distinct()
        if (dnsList != currentDnsList) {
            currentDnsList = dnsList
            Core.updateDNS(dnsList.joinToString(","))
        }
        val network = preferred?.key
        // Sockets protected onto the network we left stay bound to it; closing
        // them makes apps reconnect now instead of after a TCP timeout.
        val capabilities = network?.let { connectivity?.getNetworkCapabilities(it) }
        val transport = when {
            capabilities?.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) == true -> "wifi"
            capabilities?.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) == true -> "cellular"
            else -> "other"
        }
        val interfaceName = network?.let { connectivity?.getLinkProperties(it)?.interfaceName }.orEmpty()
        val key = if (network == null) "" else "$transport|$interfaceName|${dnsList.sorted().joinToString(",")}"
        if (currentNetwork != network || currentNetworkKey != key) {
            Core.networkChanged(key)
        }
        currentNetwork = network
        currentNetworkKey = key
    }

    @Synchronized
    override fun stop() {
        active = false
        handler.removeCallbacksAndMessages(null)
        try {
            if (receiverRegistered) {
                receiverRegistered = false
                service.unregisterReceiver(wakeReceiver)
            }
        } finally {
            try {
                if (callbackRegistered) {
                    callbackRegistered = false
                    connectivity?.unregisterNetworkCallback(callback)
                }
            } finally {
                networkInfos.clear()
                updateDns(force = true)
                currentNetwork = null
            }
        }
    }
}

private const val DNS_PORT = 53
private const val DNS_UPDATE_DELAY_MILLIS = 200L
private const val WAKE_CHECK_DELAY_MILLIS = 500L
private const val RECOVERY_CHECK_INTERVAL_MILLIS = 30_000L

private val networkObserveLooper: Looper by lazy {
    HandlerThread("NetworkObserve").apply { start() }.looper
}

private fun InetAddress.asSocketAddressText(port: Int): String = when (this) {
    is Inet6Address -> "[$hostAddress]:$port"
    is Inet4Address -> "$hostAddress:$port"
    else -> error("Unsupported address type: ${javaClass.name}")
}
