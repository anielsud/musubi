package com.musubi.phone.net

import com.musubi.phone.core.AddressFilter
import com.musubi.phone.core.FrameReader
import java.io.IOException
import java.net.InetSocketAddress
import java.net.Socket
import java.net.SocketException
import java.security.MessageDigest
import java.security.Principal
import java.security.PrivateKey
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit
import javax.net.ssl.KeyManager
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLEngine
import javax.net.ssl.SSLServerSocket
import javax.net.ssl.SSLSocket
import javax.net.ssl.X509ExtendedKeyManager
import javax.net.ssl.X509TrustManager

/**
 * Who may complete the TLS handshake: the pinned Mac; any client certificate only while pairing
 * is open (the session then allows the pairing ops only). A refused client fails the handshake.
 */
class ClientTrust(
    private val pin: () -> ByteArray?,
    private val pairingOpen: () -> Boolean,
    private val onRefused: () -> Unit,
) : X509TrustManager {
    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        val leaf = chain?.firstOrNull() ?: throw CertificateException("no client certificate")
        val fp = MessageDigest.getInstance("SHA-256").digest(leaf.encoded)
        val p = pin()
        if (p != null && MessageDigest.isEqual(p, fp)) return
        if (pairingOpen()) return
        onRefused()
        throw CertificateException("client certificate is not the paired Mac")
    }

    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) =
        throw CertificateException("server role only")

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}

/** Serves the Keystore key and its certificate, whatever key type the TLS stack asks about. */
private class IdentityKeyManager(private val id: PhoneIdentity) : X509ExtendedKeyManager() {
    override fun chooseServerAlias(keyType: String?, issuers: Array<out Principal>?, socket: Socket?) = PhoneIdentity.ALIAS
    override fun chooseEngineServerAlias(keyType: String?, issuers: Array<out Principal>?, engine: SSLEngine?) = PhoneIdentity.ALIAS
    override fun getServerAliases(keyType: String?, issuers: Array<out Principal>?) = arrayOf(PhoneIdentity.ALIAS)
    override fun getCertificateChain(alias: String?): Array<X509Certificate> = arrayOf(id.certificate)
    override fun getPrivateKey(alias: String?): PrivateKey = id.privateKey
    override fun getClientAliases(keyType: String?, issuers: Array<out Principal>?): Array<String>? = null
    override fun chooseClientAlias(keyType: Array<out String>?, issuers: Array<out Principal>?, socket: Socket?): String? = null
}

/**
 * The phone's TLS server (docs/phone.md, "Sync: the Mac dials the phone"): port 47616, TLS 1.2
 * or 1.3 with ECDHE and AES-GCM or ChaCha20 only, a client certificate required. Connections
 * from non-private addresses are closed before the handshake. One connection at a time: the
 * accept loop serves each connection itself. Reads time out (idle and per frame) and a
 * watchdog closes a connection whose request takes too long, so a stalled peer cannot hold it.
 */
class TlsServer(
    private val identity: PhoneIdentity,
    private val trust: ClientTrust,
    private val serve: (socket: SSLSocket, macFp: ByteArray, reader: FrameReader) -> Unit,
    /** How long to wait for the next request; longer while the owners compare a pairing code. */
    private val idleMs: () -> Int = { IDLE_MS },
    private val port: Int = PORT,
) {
    @Volatile private var server: SSLServerSocket? = null
    @Volatile var busy = false
        private set
    private val watchdog = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "musubi-sync-watchdog").apply { isDaemon = true } }

    val isRunning: Boolean get() = server?.isClosed == false

    @Synchronized
    fun start() {
        if (isRunning) return
        val ctx = SSLContext.getInstance("TLS")
        ctx.init(arrayOf<KeyManager>(IdentityKeyManager(identity)), arrayOf(trust), null)
        val s = ctx.serverSocketFactory.createServerSocket() as SSLServerSocket
        s.reuseAddress = true
        s.bind(InetSocketAddress(port), 1)
        s.needClientAuth = true
        s.enabledProtocols = PROTOCOLS.filter { it in s.supportedProtocols }.toTypedArray()
        s.enabledCipherSuites = SUITES.filter { it in s.supportedCipherSuites }.toTypedArray()
        server = s
        Thread({ loop(s) }, "musubi-sync-server").apply { isDaemon = true }.start()
    }

    /** Stops accepting; a connection in progress finishes (bounded by its timeouts). */
    @Synchronized
    fun stop() {
        server?.let { runCatching { it.close() } }
        server = null
    }

    private fun loop(s: SSLServerSocket) {
        while (!s.isClosed) {
            val sock = try {
                s.accept() as SSLSocket
            } catch (e: IOException) {
                if (s.isClosed) return
                continue
            }
            if (!AddressFilter.isAllowed(sock.inetAddress)) {
                runCatching { sock.close() }
                continue
            }
            busy = true
            try {
                handle(sock)
            } finally {
                busy = false
                runCatching { sock.close() }
            }
        }
    }

    private fun handle(sock: SSLSocket) {
        var dog: ScheduledFuture<*>? = null
        fun arm(ms: Long) {
            dog?.cancel(false)
            dog = watchdog.schedule({ runCatching { sock.close() } }, ms, TimeUnit.MILLISECONDS)
        }
        try {
            sock.soTimeout = HANDSHAKE_MS
            arm(HANDSHAKE_MS * 2L)
            val macFp = try {
                sock.startHandshake()
                val leaf = sock.session.peerCertificates.firstOrNull() as? X509Certificate ?: return
                MessageDigest.getInstance("SHA-256").digest(leaf.encoded)
            } catch (e: IOException) {
                return // ClientTrust has recorded a refused certificate; nothing else to say.
            }
            val reader = FrameReader(sock.inputStream.buffered(), object : FrameReader.Timeouts {
                override fun idle() {
                    dog?.cancel(false)
                    sock.soTimeout = idleMs()
                }

                override fun inFrame(bytes: Long) {
                    sock.soTimeout = FRAME_READ_MS
                    // Covers reading the rest of the request, handling it and writing the reply
                    // (a pair_confirm waits up to 2 minutes for the owner; a chunk can be large).
                    arm(REQUEST_MS + bytes / MIN_BYTES_PER_MS)
                }

                override fun check() {}
            })
            serve(sock, macFp, reader)
        } catch (e: SocketException) {
            // Closed by the watchdog or the peer.
        } catch (e: IOException) {
            // Timeouts and protocol violations end the connection; the caller records the outcome.
        } finally {
            dog?.cancel(false)
        }
    }

    companion object {
        const val PORT = 47616
        private const val HANDSHAKE_MS = 10_000
        const val IDLE_MS = 60_000
        private const val FRAME_READ_MS = 15_000
        private const val REQUEST_MS = 3 * 60_000L
        /** At least 64 KB/s for bodies before the watchdog gives up. */
        private const val MIN_BYTES_PER_MS = 64L
        private val PROTOCOLS = listOf("TLSv1.3", "TLSv1.2")
        /** TLS 1.2 suites; TLS 1.3's (all AEAD with ECDHE here) are always on in Conscrypt. */
        private val SUITES = listOf(
            "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
            "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
            "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256",
            "TLS_AES_128_GCM_SHA256",
            "TLS_AES_256_GCM_SHA384",
            "TLS_CHACHA20_POLY1305_SHA256",
        )
    }
}
