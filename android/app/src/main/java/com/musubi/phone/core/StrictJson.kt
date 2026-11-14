package com.musubi.phone.core

import java.nio.ByteBuffer
import java.nio.charset.CharacterCodingException
import java.nio.charset.CodingErrorAction

/**
 * A strict RFC 8259 JSON parser for everything the Mac sends. Unlike org.json (lenient: unquoted
 * strings, comments, single quotes, and different on the JVM and on Android), it accepts only
 * well-formed JSON in valid UTF-8, rejects duplicate keys and limits nesting. Objects become
 * [Map]s (insertion order kept), arrays [List]s, integers [Long], other numbers [Double].
 */
object StrictJson {
    class Malformed(msg: String) : Exception(msg)

    const val MAX_DEPTH = 32

    fun parse(bytes: ByteArray): Any? {
        val text = try {
            Charsets.UTF_8.newDecoder()
                .onMalformedInput(CodingErrorAction.REPORT)
                .onUnmappableCharacter(CodingErrorAction.REPORT)
                .decode(ByteBuffer.wrap(bytes)).toString()
        } catch (e: CharacterCodingException) {
            throw Malformed("not UTF-8")
        }
        return parse(text)
    }

    fun parse(text: String): Any? {
        val p = Parser(text)
        p.ws()
        val v = p.value(0)
        p.ws()
        if (p.i != text.length) throw Malformed("trailing data")
        return v
    }

    /** Parses and requires a top-level object. */
    fun parseObject(bytes: ByteArray): Map<String, Any?> {
        @Suppress("UNCHECKED_CAST")
        return parse(bytes) as? Map<String, Any?> ?: throw Malformed("not an object")
    }

    private class Parser(val s: String) {
        var i = 0

        fun ws() {
            while (i < s.length && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r')) i++
        }

        fun value(depth: Int): Any? {
            if (i >= s.length) throw Malformed("unexpected end")
            return when (val c = s[i]) {
                '{' -> obj(depth + 1)
                '[' -> arr(depth + 1)
                '"' -> str()
                't' -> lit("true", true)
                'f' -> lit("false", false)
                'n' -> lit("null", null)
                else -> if (c == '-' || c in '0'..'9') num() else throw Malformed("unexpected character")
            }
        }

        private fun lit(word: String, v: Any?): Any? {
            if (!s.startsWith(word, i)) throw Malformed("bad literal")
            i += word.length
            return v
        }

        private fun obj(depth: Int): Map<String, Any?> {
            if (depth > MAX_DEPTH) throw Malformed("too deep")
            i++ // {
            val m = LinkedHashMap<String, Any?>()
            ws()
            if (i < s.length && s[i] == '}') { i++; return m }
            while (true) {
                ws()
                if (i >= s.length || s[i] != '"') throw Malformed("expected key")
                val k = str()
                if (m.containsKey(k)) throw Malformed("duplicate key")
                ws()
                if (i >= s.length || s[i] != ':') throw Malformed("expected colon")
                i++
                ws()
                m[k] = value(depth)
                ws()
                if (i >= s.length) throw Malformed("unexpected end")
                when (s[i]) {
                    ',' -> i++
                    '}' -> { i++; return m }
                    else -> throw Malformed("expected , or }")
                }
            }
        }

        private fun arr(depth: Int): List<Any?> {
            if (depth > MAX_DEPTH) throw Malformed("too deep")
            i++ // [
            val out = ArrayList<Any?>()
            ws()
            if (i < s.length && s[i] == ']') { i++; return out }
            while (true) {
                ws()
                out += value(depth)
                ws()
                if (i >= s.length) throw Malformed("unexpected end")
                when (s[i]) {
                    ',' -> i++
                    ']' -> { i++; return out }
                    else -> throw Malformed("expected , or ]")
                }
            }
        }

        private fun str(): String {
            i++ // opening quote
            val sb = StringBuilder()
            while (true) {
                if (i >= s.length) throw Malformed("unterminated string")
                val c = s[i++]
                when {
                    c == '"' -> return sb.toString()
                    c == '\\' -> {
                        if (i >= s.length) throw Malformed("bad escape")
                        when (s[i++]) {
                            '"' -> sb.append('"')
                            '\\' -> sb.append('\\')
                            '/' -> sb.append('/')
                            'b' -> sb.append('\b')
                            'f' -> sb.append('\u000c')
                            'n' -> sb.append('\n')
                            'r' -> sb.append('\r')
                            't' -> sb.append('\t')
                            'u' -> {
                                if (i + 4 > s.length) throw Malformed("bad escape")
                                val h = s.substring(i, i + 4)
                                if (!h.all { it in '0'..'9' || it in 'a'..'f' || it in 'A'..'F' }) throw Malformed("bad escape")
                                sb.append(h.toInt(16).toChar())
                                i += 4
                            }
                            else -> throw Malformed("bad escape")
                        }
                    }
                    c < ' ' -> throw Malformed("control character in string")
                    else -> sb.append(c)
                }
            }
        }

        private fun num(): Any {
            val start = i
            if (s[i] == '-') i++
            if (i >= s.length) throw Malformed("bad number")
            if (s[i] == '0') i++
            else if (s[i] in '1'..'9') while (i < s.length && s[i] in '0'..'9') i++
            else throw Malformed("bad number")
            var integral = true
            if (i < s.length && s[i] == '.') {
                integral = false
                i++
                if (i >= s.length || s[i] !in '0'..'9') throw Malformed("bad number")
                while (i < s.length && s[i] in '0'..'9') i++
            }
            if (i < s.length && (s[i] == 'e' || s[i] == 'E')) {
                integral = false
                i++
                if (i < s.length && (s[i] == '+' || s[i] == '-')) i++
                if (i >= s.length || s[i] !in '0'..'9') throw Malformed("bad number")
                while (i < s.length && s[i] in '0'..'9') i++
            }
            val t = s.substring(start, i)
            if (t.length > 40) throw Malformed("number too long")
            return if (integral) t.toLongOrNull() ?: throw Malformed("integer out of range") else t.toDouble()
        }
    }
}
