package com.follow.clash.service

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
import android.os.PowerManager
import androidx.core.content.ContextCompat
import androidx.core.content.getSystemService
import com.follow.clash.common.BroadcastAction
import com.follow.clash.common.GlobalState
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

// Outlives every service run: the Core also tests delays and updates providers
// while stopped, and a `system` nameserver has no upstream until DNS arrives here.
object NetworkObserver {

    private val networkInfos = ConcurrentHashMap<Network, NetworkInfo>()
    private val connectivity by lazy {
        GlobalState.application.getSystemService<ConnectivityManager>()
    }
    private val handler by lazy {
        Handler(HandlerThread("NetworkObserve").apply { start() }.looper)
    }
    private var currentDnsList = listOf<String>()
    private var currentNetwork: Network? = null
    private var currentNetworkKey = ""
    private var started = false
    @Volatile
    private var monitoringService: Service? = null
    private var receiverRegistered = false

    private val wakeCheckTask = Runnable {
        synchronized(this) {
            if (monitoringService != null) {
                refreshNetworks()
                updateDns()
                Core.smartRecheck()
            }
        }
    }

    private val recoveryTask = object : Runnable {
        override fun run() {
            synchronized(this@NetworkObserver) {
                if (monitoringService == null) return
                if (Core.takeSmartRestartRequest()) {
                    BroadcastAction.VPN_RECOVERY_REQUESTED.sendBroadcast()
                }
                handler.postDelayed(this, RECOVERY_CHECK_INTERVAL_MILLIS)
            }
        }
    }

    private val wakeReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            if (monitoringService == null) return
            if (intent?.action == PowerManager.ACTION_DEVICE_IDLE_MODE_CHANGED &&
                GlobalState.application.getSystemService<PowerManager>()?.isDeviceIdleMode == true
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
        handler.removeCallbacks(updateDnsTask)
        handler.postDelayed(updateDnsTask, DNS_UPDATE_DELAY_MILLIS)
    }

    @Synchronized
    fun start() {
        if (started) return
        runCatching { requireNotNull(connectivity).registerNetworkCallback(request, callback) }
            .onSuccess {
                started = true
                handler.post {
                    refreshNetworks()
                    updateDns()
                }
            }
            .onFailure { error -> GlobalState.log("Unable to observe networks: $error") }
    }

    @Synchronized
    internal fun startSmartMonitoring(service: Service) {
        if (monitoringService === service) return
        start()
        if (!receiverRegistered) {
            ContextCompat.registerReceiver(
                GlobalState.application,
                wakeReceiver,
                IntentFilter().apply {
                    addAction(Intent.ACTION_SCREEN_ON)
                    addAction(Intent.ACTION_USER_PRESENT)
                    addAction(PowerManager.ACTION_DEVICE_IDLE_MODE_CHANGED)
                },
                ContextCompat.RECEIVER_EXPORTED,
            )
            receiverRegistered = true
        }
        monitoringService = service
        handler.removeCallbacks(recoveryTask)
        handler.postDelayed(recoveryTask, RECOVERY_CHECK_INTERVAL_MILLIS)
    }

    @Synchronized
    internal fun stopSmartMonitoring(service: Service) {
        if (monitoringService !== service) return
        monitoringService = null
        handler.removeCallbacks(wakeCheckTask)
        handler.removeCallbacks(recoveryTask)
        if (receiverRegistered) {
            receiverRegistered = false
            runCatching { GlobalState.application.unregisterReceiver(wakeReceiver) }
                .onFailure { error -> GlobalState.log("Unable to stop Smart wake observation: $error") }
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
    private fun updateDns() {
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
}

private const val DNS_PORT = 53
private const val DNS_UPDATE_DELAY_MILLIS = 200L
private const val WAKE_CHECK_DELAY_MILLIS = 500L
private const val RECOVERY_CHECK_INTERVAL_MILLIS = 30_000L

private fun InetAddress.asSocketAddressText(port: Int): String = when (this) {
    is Inet6Address -> "[$hostAddress]:$port"
    is Inet4Address -> "$hostAddress:$port"
    else -> error("Unsupported address type: ${javaClass.name}")
}
