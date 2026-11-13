package com.musubi.phone.ui

import android.content.Context
import android.graphics.Typeface
import android.util.TypedValue
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import com.google.android.material.button.MaterialButton
import com.google.android.material.card.MaterialCardView
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

/** Tiny view helpers so screens stay short. Plain Android views, no extra libraries. */
fun Context.dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()

fun Context.vbox(padDp: Int = 0): LinearLayout = LinearLayout(this).apply {
    orientation = LinearLayout.VERTICAL
    setPadding(dp(padDp), dp(padDp), dp(padDp), dp(padDp))
}

fun Context.label(text: CharSequence, sizeSp: Float = 15f, bold: Boolean = false, secondary: Boolean = false): TextView =
    TextView(this).apply {
        this.text = text
        setTextSize(TypedValue.COMPLEX_UNIT_SP, sizeSp)
        if (bold) setTypeface(typeface, Typeface.BOLD)
        if (secondary) alpha = 0.7f
    }

fun Context.button(text: String, outlined: Boolean = false, onClick: () -> Unit): MaterialButton {
    val style = if (outlined) com.google.android.material.R.attr.materialButtonOutlinedStyle else com.google.android.material.R.attr.materialButtonStyle
    return MaterialButton(this, null, style).apply {
        this.text = text
        setOnClickListener { onClick() }
    }
}

fun Context.card(content: View): MaterialCardView = MaterialCardView(this).apply {
    layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).also {
        it.setMargins(0, dp(6), 0, dp(6))
    }
    addView(content)
}

fun View.marginTop(ctx: Context, dpTop: Int): View {
    val lp = (layoutParams as? LinearLayout.LayoutParams)
        ?: LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
    lp.topMargin = ctx.dp(dpTop)
    layoutParams = lp
    return this
}

object Fmt {
    private val day = DateTimeFormatter.ofPattern("EEE d MMM", Locale.getDefault())
    private val time = DateTimeFormatter.ofPattern("HH:mm", Locale.getDefault())

    fun local(t: OffsetDateTime) = t.atZoneSameInstant(ZoneId.systemDefault())
    fun day(t: OffsetDateTime): String = local(t).format(day)
    fun time(t: OffsetDateTime): String = local(t).format(time)

    fun elapsed(ms: Long): String {
        val s = ms / 1000
        return if (s >= 3600) "%d:%02d:%02d".format(s / 3600, (s / 60) % 60, s % 60) else "%02d:%02d".format(s / 60, s % 60)
    }
}
