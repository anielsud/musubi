package com.musubi.phone.ui

import android.graphics.Typeface
import android.text.SpannableStringBuilder
import android.text.Spanned
import android.text.TextPaint
import android.text.method.LinkMovementMethod
import android.text.style.BackgroundColorSpan
import android.text.style.ClickableSpan
import android.text.style.LeadingMarginSpan
import android.text.style.RelativeSizeSpan
import android.text.style.StyleSpan
import android.view.View
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.core.graphics.ColorUtils
import com.google.android.material.color.MaterialColors
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.musubi.phone.MainActivity
import com.musubi.phone.core.BriefText
import com.musubi.phone.core.OpenItem
import com.musubi.phone.core.Source

/** Briefs as the desktop shows them: bold, nested bullets, paragraphs, and [n] marks that open the source. */
object BriefUi {
    /** The brief text as views, one per paragraph or bullet. */
    fun render(a: MainActivity, text: String, sources: List<Source>): View {
        val byN = sources.associateBy { it.n }
        val box = a.vbox()
        for (b in BriefText.parse(text, byN.keys)) {
            val sb = SpannableStringBuilder()
            val depth = (b as? BriefText.Bullet)?.depth
            if (depth != null) sb.append("•  ")
            spans(a, sb, b.spans, byN)
            if (depth != null) {
                // Hanging indent: wrapped lines line up after the bullet.
                val first = a.dp(14) * depth
                val bulletWidth = TextView(a).paint.measureText("•  ").toInt() + a.dp(1)
                sb.setSpan(LeadingMarginSpan.Standard(first, first + bulletWidth), 0, sb.length, Spanned.SPAN_INCLUSIVE_EXCLUSIVE)
            }
            box.addView(textView(a, sb).marginTop(a, if (depth != null) 2 else 8))
        }
        return box
    }

    private fun spans(a: MainActivity, sb: SpannableStringBuilder, spans: List<BriefText.Span>, byN: Map<Int, Source>) {
        for (sp in spans) {
            val start = sb.length
            when (sp) {
                is BriefText.Text -> sb.append(sp.text)
                is BriefText.Cite -> {
                    val src = byN[sp.n] ?: continue
                    mark(a, sb, sp.n.toString()) { showSource(a, src) }
                }
            }
            if (sp.bold() && sb.length > start) sb.setSpan(StyleSpan(Typeface.BOLD), start, sb.length, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
        }
    }

    private fun BriefText.Span.bold() = when (this) {
        is BriefText.Text -> bold
        is BriefText.Cite -> bold
    }

    /** A small tinted, tappable number. */
    private fun mark(a: MainActivity, sb: SpannableStringBuilder, label: String, onTap: () -> Unit) {
        val color = MaterialColors.getColor(a, com.google.android.material.R.attr.colorPrimary, 0xFF3F51B5.toInt())
        sb.append(' ')
        val s = sb.length
        sb.append(" ").append(label).append(" ")
        val e = sb.length
        sb.setSpan(object : ClickableSpan() {
            override fun onClick(widget: View) = onTap()
            override fun updateDrawState(ds: TextPaint) {
                ds.color = color
                ds.isUnderlineText = false
                ds.isFakeBoldText = true
            }
        }, s, e, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
        sb.setSpan(RelativeSizeSpan(0.75f), s, e, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
        sb.setSpan(BackgroundColorSpan(ColorUtils.setAlphaComponent(color, 0x2E)), s, e, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
    }

    private fun textView(a: MainActivity, text: CharSequence, sizeSp: Float = 15f): TextView = a.label(text, sizeSp).apply {
        movementMethod = LinkMovementMethod.getInstance()
        // LinkMovementMethod would otherwise leave the tapped mark highlighted.
        highlightColor = 0
    }

    /** "Open between us": each item with who, due and its marks, then its sources as tappable marks. */
    fun openItems(a: MainActivity, items: List<OpenItem>): View {
        val box = a.vbox()
        box.addView(a.label("Open between us", 16f, bold = true))
        items.forEachIndexed { i, o ->
            if (o.quiet && (i == 0 || !items[i - 1].quiet)) {
                box.addView(a.label("Gone quiet: nothing said of these since; check before raising", 13f, secondary = true).marginTop(a, 10))
            }
            val sb = SpannableStringBuilder(o.text)
            for (s in o.sources) mark(a, sb, s.n.toString()) { showSource(a, s) }
            val item = a.vbox()
            item.addView(textView(a, sb).apply { if (o.quiet) alpha = 0.75f })
            val tags = buildList {
                if (o.overdue) add("overdue")
                if (o.private) add("for you")
            }
            val meta = listOfNotNull(
                o.who.takeIf { it.isNotBlank() },
                o.due.takeIf { it.isNotBlank() }?.let { "due $it" },
                o.raised?.let { "raised ${Fmt.day(it)}" },
                o.thread.takeIf { it.isNotBlank() },
            )
            val line = (tags.map { it.uppercase() } + meta).joinToString(" · ")
            if (line.isNotEmpty()) item.addView(a.label(line, 12f, secondary = true))
            box.addView(item.marginTop(a, 8))
        }
        return box
    }

    /** The source view: title, date, speakers, passage and its label. */
    fun showSource(a: MainActivity, s: Source) {
        val box = a.vbox(20)
        val when_ = s.occurredAt?.let { "${Fmt.day(it)} ${Fmt.time(it)}" }
        box.addView(a.label(listOfNotNull(s.kind.takeIf { it.isNotBlank() }, when_).joinToString(" · "), 13f, secondary = true))
        if (s.speakers.isNotBlank()) box.addView(a.label("Speakers: ${s.speakers}", 13f, secondary = true).marginTop(a, 4))
        box.addView(a.label(s.text.ifBlank { "(no passage)" }, 15f).apply { setTextIsSelectable(true) }.marginTop(a, 12))
        box.addView(a.label("Sensitivity: ${s.sensitivity.level} · share: ${s.sensitivity.share}", 13f, bold = true).marginTop(a, 16))
        shareHelp(s.sensitivity.share)?.let { box.addView(a.label(it, 12f, secondary = true)) }
        if (s.sensitivity.why.isNotBlank()) box.addView(a.label("Why: ${s.sensitivity.why}", 12f))
        val scroll = ScrollView(a).apply { addView(box, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT)) }
        MaterialAlertDialogBuilder(a)
            .setTitle("${s.n}. ${s.title.ifBlank { "Source" }}")
            .setView(scroll)
            .setPositiveButton("Close", null)
            .show()
    }

    fun shareHelp(share: String) = when (share) {
        "public" -> "Anyone may hear this."
        "internal" -> "Colleagues only, not people outside the company."
        "party-only" -> "Only people who were party to the source."
        "owner-only" -> "Personal details: for you only."
        else -> null
    }
}
