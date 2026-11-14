package com.musubi.phone.core

/**
 * The little markdown briefs use (docs/phone.md, "Agenda JSON"), read as the desktop's Answer reads
 * it: paragraphs, bullet lists (`-`/`*`, nested by two spaces), `**bold**`, a leading `#` dropped,
 * and `[n]` citations. A citation with no matching source is dropped. Pure, so it runs in JVM tests.
 */
object BriefText {
    sealed interface Span
    data class Text(val text: String, val bold: Boolean = false) : Span
    data class Cite(val n: Int, val bold: Boolean = false) : Span

    sealed interface Block { val spans: List<Span> }
    data class Para(override val spans: List<Span>) : Block
    data class Bullet(val depth: Int, override val spans: List<Span>) : Block

    const val MAX_DEPTH = 6

    private val bullet = Regex("^(\\s*)[*-]\\s+(.*)$")
    private val bold = Regex("\\*\\*[^*]+\\*\\*")
    private val cite = Regex("\\[(\\d{1,6})]")

    fun parse(text: String, sources: Set<Int>): List<Block> {
        val out = ArrayList<Block>()
        for (line in text.split('\n')) {
            val m = bullet.find(line)
            if (m != null) {
                out += Bullet((m.groupValues[1].length / 2).coerceAtMost(MAX_DEPTH), inline(m.groupValues[2], sources))
                continue
            }
            if (line.isNotBlank()) out += Para(inline(line.replace(Regex("^#+\\s*"), ""), sources))
        }
        return out
    }

    fun inline(s: String, sources: Set<Int>): List<Span> {
        val out = ArrayList<Span>()
        var last = 0
        for (m in bold.findAll(s)) {
            if (m.range.first > last) cites(s.substring(last, m.range.first), false, sources, out)
            cites(m.value.substring(2, m.value.length - 2), true, sources, out)
            last = m.range.last + 1
        }
        if (last < s.length) cites(s.substring(last), false, sources, out)
        return merge(out)
    }

    private fun cites(s: String, isBold: Boolean, sources: Set<Int>, out: MutableList<Span>) {
        var last = 0
        for (m in cite.findAll(s)) {
            var before = s.substring(last, m.range.first)
            val n = m.groupValues[1].toInt()
            if (n in sources) {
                if (before.isNotEmpty()) out += Text(before, isBold)
                out += Cite(n, isBold)
            } else {
                // Dropped: so is the space before it when punctuation, a space or the end follows.
                val next = s.getOrNull(m.range.last + 1)
                if (before.endsWith(' ') && (next == null || next == ' ' || next in ".,;:!?)")) before = before.dropLast(1)
                if (before.isNotEmpty()) out += Text(before, isBold)
            }
            last = m.range.last + 1
        }
        if (last < s.length) out += Text(s.substring(last), isBold)
    }

    private fun merge(spans: List<Span>): List<Span> {
        val out = ArrayList<Span>()
        for (sp in spans) {
            val prev = out.lastOrNull()
            if (sp is Text && prev is Text && prev.bold == sp.bold) out[out.size - 1] = Text(prev.text + sp.text, sp.bold)
            else out += sp
        }
        return out
    }
}
