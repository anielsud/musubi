package com.musubi.phone.net

import com.musubi.phone.core.Hashing
import java.net.InetSocketAddress
import java.net.Socket
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLSocket
import javax.net.ssl.SSLSocketFactory
import javax.net.ssl.X509TrustManager

/**
 * TLS to the Mac trusts exactly one certificate: the one whose SHA-256 (over its DER bytes)
 * was shown to the owner and pinned at pairing. No CA, no other certificate, no fallback.
 */
object Pinning {
    private val PROTOCOLS = arrayOf("TLSv1.3", "TLSv1.2")

    fun fingerprint(cert: X509Certificate): String = Hashing.sha256Hex(cert.encoded)

    /** First 16 hex digits, grouped for reading aloud: "ab12 cd34 ef56 7890". */
    fun shortForm(fingerprint: String): String = fingerprint.take(16).chunked(4).joinToString(" ")

    private fun matches(cert: X509Certificate?, pin: String): Boolean {
        if (cert == null) return false
        return MessageDigest.isEqual(fingerprint(cert).toByteArray(), pin.lowercase().toByteArray())
    }

    private class PinnedTrustManager(private val pin: String) : X509TrustManager {
        override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) =
            throw CertificateException("client certificates not used")

        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
            if (!matches(chain?.firstOrNull(), pin)) throw CertificateException("certificate does not match the paired Mac")
        }

        override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
    }

    /** Records the server's certificate and then refuses it, so the handshake ends before any data is sent. */
    private class CaptureTrustManager : X509TrustManager {
        @Volatile var captured: X509Certificate? = null
        override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) =
            throw CertificateException("client certificates not used")

        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
            captured = chain?.firstOrNull()
            throw CertificateException("capture only")
        }

        override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
    }

    fun socketFactory(pin: String): SSLSocketFactory {
        val ctx = SSLContext.getInstance("TLS")
        ctx.init(null, arrayOf(PinnedTrustManager(pin)), null)
        return ModernTls(ctx.socketFactory)
    }

    /**
     * Hostname checks cannot apply to a self-signed certificate reached by IP; the pin is the
     * identity check. This verifier re-checks the pin on the established session.
     */
    fun hostnameVerifier(pin: String): HostnameVerifier = HostnameVerifier { _, session ->
        val cert = runCatching { session.peerCertificates.firstOrNull() as? X509Certificate }.getOrNull()
        matches(cert, pin)
    }

    /** Opens a TLS handshake to [host]:[port] only to read the certificate's fingerprint. */
    fun fetchFingerprint(host: String, port: Int, timeoutMs: Int = 5000): String {
        val tm = CaptureTrustManager()
        val ctx = SSLContext.getInstance("TLS")
        ctx.init(null, arrayOf(tm), null)
        val plain = Socket()
        try {
            plain.connect(InetSocketAddress(host, port), timeoutMs)
            plain.soTimeout = timeoutMs
            val s = ModernTls(ctx.socketFactory).createSocket(plain, host, port, true) as SSLSocket
            runCatching { s.startHandshake() }
            runCatching { s.close() }
        } finally {
            runCatching { plain.close() }
        }
        val cert = tm.captured ?: throw java.io.IOException("No certificate received from the Mac")
        return fingerprint(cert)
    }

    /** Restricts sockets to TLS 1.2 and 1.3. */
    private class ModernTls(private val d: SSLSocketFactory) : SSLSocketFactory() {
        private fun fix(s: Socket): Socket {
            if (s is SSLSocket) {
                val wanted = PROTOCOLS.filter { it in s.supportedProtocols }.toTypedArray()
                s.enabledProtocols = wanted
            }
            return s
        }

        override fun getDefaultCipherSuites(): Array<String> = d.defaultCipherSuites
        override fun getSupportedCipherSuites(): Array<String> = d.supportedCipherSuites
        override fun createSocket(s: Socket?, host: String?, port: Int, autoClose: Boolean) = fix(d.createSocket(s, host, port, autoClose))
        override fun createSocket(host: String?, port: Int) = fix(d.createSocket(host, port))
        override fun createSocket(host: String?, port: Int, localHost: java.net.InetAddress?, localPort: Int) =
            fix(d.createSocket(host, port, localHost, localPort))
        override fun createSocket(host: java.net.InetAddress?, port: Int) = fix(d.createSocket(host, port))
        override fun createSocket(address: java.net.InetAddress?, port: Int, localAddress: java.net.InetAddress?, localPort: Int) =
            fix(d.createSocket(address, port, localAddress, localPort))
    }
}
