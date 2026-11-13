package com.musubi.phone.net

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Build
import com.musubi.phone.core.DiscoveryParse
import com.musubi.phone.core.FoundMac
import java.net.Inet4Address
import java.net.Inet6Address
import java.net.InetAddress
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Finds Macs advertising `_musubi._tcp` on the local network (Android NSD). Each service found
 * is resolved one at a time (older Android resolves only one at once) and reported with its
 * instance name, TXT fingerprint and address. Needs no permission at targetSdk 35.
 */
class MacDiscovery(context: Context, private val onFound: (FoundMac) -> Unit) {
    private val nsd = context.applicationContext.getSystemService(NsdManager::class.java)
    private val queue = ArrayDeque<NsdServiceInfo>()
    private var resolving = false
    private var active = false

    private val discovery = object : NsdManager.DiscoveryListener {
        override fun onDiscoveryStarted(serviceType: String) {}
        override fun onDiscoveryStopped(serviceType: String) {}
        override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) { synchronized(this@MacDiscovery) { active = false } }
        override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {}
        override fun onServiceLost(info: NsdServiceInfo) {}
        override fun onServiceFound(info: NsdServiceInfo) {
            if (DiscoveryParse.cleanName(info.serviceName) == null) return
            synchronized(this@MacDiscovery) {
                if (queue.size < MAX_QUEUE) queue.addLast(info)
            }
            next()
        }
    }

    fun start() {
        synchronized(this) {
            if (active) return
            active = true
        }
        runCatching { nsd.discoverServices(DiscoveryParse.SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, discovery) }
            .onFailure { synchronized(this) { active = false } }
    }

    fun stop() {
        synchronized(this) {
            if (!active) return
            active = false
            queue.clear()
        }
        runCatching { nsd.stopServiceDiscovery(discovery) }
    }

    @Suppress("DEPRECATION")
    private fun next() {
        val info = synchronized(this) {
            if (resolving || !active) return
            queue.removeFirstOrNull()?.also { resolving = true } ?: return
        }
        val listener = object : NsdManager.ResolveListener {
            override fun onResolveFailed(s: NsdServiceInfo, errorCode: Int) = done()
            override fun onServiceResolved(s: NsdServiceInfo) {
                report(s)
                done()
            }
        }
        runCatching { nsd.resolveService(info, listener) }.onFailure { done() }
    }

    private fun done() {
        synchronized(this) { resolving = false }
        next()
    }

    @Suppress("DEPRECATION")
    private fun report(s: NsdServiceInfo) {
        val name = DiscoveryParse.cleanName(s.serviceName) ?: return
        val fp = DiscoveryParse.fingerprint(s.attributes ?: emptyMap()) ?: return
        if (s.port !in 1..65535) return
        val addrs: List<InetAddress> = if (Build.VERSION.SDK_INT >= 34) s.hostAddresses else listOfNotNull(s.host)
        val host = pickAddress(addrs) ?: return
        onFound(FoundMac(name, fp, host, s.port))
    }

    companion object {
        private const val MAX_QUEUE = 32

        /**
         * IPv4 first; IPv6 only when it needs no interface scope (URLs cannot carry one reliably).
         * Never loopback: a Mac's own 127.0.0.1 record can leak into answers (seen on the emulator).
         */
        fun pickAddress(all: List<InetAddress>): String? {
            val addrs = all.filter { !it.isLoopbackAddress && !it.isAnyLocalAddress && !it.isMulticastAddress }
            return addrs.firstOrNull { it is Inet4Address }?.hostAddress
                ?: addrs.firstOrNull { it is Inet6Address && !it.isLinkLocalAddress }?.hostAddress?.substringBefore('%')
        }

        /** Searches for up to [timeoutMs] and returns the Macs found with this exact instance name. */
        fun find(context: Context, name: String, timeoutMs: Long): List<FoundMac> {
            val found = ArrayList<FoundMac>()
            val latch = CountDownLatch(1)
            val d = MacDiscovery(context) { m ->
                if (m.name == name) {
                    synchronized(found) { found += m }
                    latch.countDown()
                }
            }
            d.start()
            try {
                latch.await(timeoutMs, TimeUnit.MILLISECONDS)
            } finally {
                d.stop()
            }
            return synchronized(found) { found.toList() }
        }
    }
}
