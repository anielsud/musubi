package com.musubi.phone

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import com.google.android.material.appbar.MaterialToolbar
import com.google.android.material.bottomnavigation.BottomNavigationView
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.musubi.phone.core.EventRef
import com.musubi.phone.rec.RecorderConfig
import com.musubi.phone.rec.RecorderService
import com.musubi.phone.rec.RecorderStatus
import com.musubi.phone.ui.BriefScreen
import com.musubi.phone.ui.MacScreen
import com.musubi.phone.ui.RecordingScreen
import com.musubi.phone.ui.RecordingsScreen
import com.musubi.phone.ui.Screen
import com.musubi.phone.ui.SettingsScreen
import com.musubi.phone.ui.TodayScreen
import com.musubi.phone.core.AgendaEvent
import com.musubi.phone.sync.PhoneSync
import com.musubi.phone.update.Updater

/** The single activity: three tabs (Today, Recordings, Mac) plus pushed Recording and Brief screens. */
class MainActivity : AppCompatActivity() {
    private lateinit var toolbar: MaterialToolbar
    private lateinit var content: FrameLayout
    private lateinit var nav: BottomNavigationView
    private val stack = ArrayDeque<Screen>()
    private var tab: Screen? = null
    private val tick = Handler(Looper.getMainLooper())
    private var pendingEvent: EventRef? = null
    private var pendingRecord = false
    private var settingsItem: android.view.MenuItem? = null

    private val permissions = registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { res ->
        if (pendingRecord) {
            pendingRecord = false
            if (res[Manifest.permission.RECORD_AUDIO] == true || hasMic()) startRecording(pendingEvent)
            else toast("Musubi needs the microphone to record")
        } else {
            current()?.refresh()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        toolbar = MaterialToolbar(this)
        content = FrameLayout(this)
        nav = BottomNavigationView(this)
        nav.menu.add(0, TAB_TODAY, 0, "Today").setIcon(R.drawable.ic_today)
        nav.menu.add(0, TAB_RECORDINGS, 1, "Recordings").setIcon(R.drawable.ic_list)
        nav.menu.add(0, TAB_MAC, 2, "Mac").setIcon(R.drawable.ic_laptop)
        root.addView(toolbar, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        root.addView(content, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        root.addView(nav, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        setContentView(root)
        // targetSdk 35 draws edge to edge: keep content clear of the system bars.
        ViewCompat.setOnApplyWindowInsetsListener(root) { v, insets ->
            val b = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout())
            v.setPadding(b.left, b.top, b.right, b.bottom)
            WindowInsetsCompat.CONSUMED
        }
        toolbar.setNavigationOnClickListener { pop() }
        settingsItem = toolbar.menu.add("Settings").setOnMenuItemClickListener { push(SettingsScreen(this)); true }
        nav.setOnItemSelectedListener { item -> showTab(item.itemId); true }
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (stack.isNotEmpty()) pop() else {
                    isEnabled = false
                    onBackPressedDispatcher.onBackPressed()
                    isEnabled = true
                }
            }
        })
        nav.selectedItemId = TAB_TODAY
        Updater.checkOnStart(this)
    }

    // The app in the foreground is a listening window: opening the app is how the owner syncs now.
    override fun onStart() {
        super.onStart()
        PhoneSync.foreground(true)
    }

    override fun onStop() {
        PhoneSync.foreground(false)
        super.onStop()
    }

    override fun onResume() {
        super.onResume()
        if (RecorderStatus.isRecording && stack.none { it is RecordingScreen }) push(RecordingScreen(this))
        current()?.refresh()
        tick.post(ticker)
        Updater.onResume(this)
    }

    override fun onPause() {
        tick.removeCallbacks(ticker)
        Updater.onPause(this)
        super.onPause()
    }

    private val ticker = object : Runnable {
        override fun run() {
            current()?.tick()
            RecorderStatus.lastError?.let {
                RecorderStatus.lastError = null
                MaterialAlertDialogBuilder(this@MainActivity).setMessage(it).setPositiveButton("OK", null).show()
            }
            tick.postDelayed(this, 250)
        }
    }

    private fun current(): Screen? = stack.lastOrNull() ?: tab

    private fun showTab(id: Int) {
        stack.clear()
        tab = when (id) {
            TAB_RECORDINGS -> RecordingsScreen(this)
            TAB_MAC -> MacScreen(this)
            else -> TodayScreen(this)
        }
        render()
    }

    fun push(s: Screen) {
        stack.addLast(s)
        render()
    }

    fun pop() {
        if (stack.isEmpty()) return
        stack.removeLast()
        render()
    }

    private fun render() {
        val s = current() ?: return
        content.removeAllViews()
        content.addView(s.view, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        toolbar.title = s.title
        toolbar.navigationIcon = if (stack.isNotEmpty()) ContextCompat.getDrawable(this, R.drawable.ic_back) else null
        nav.visibility = if (stack.isEmpty()) View.VISIBLE else View.GONE
        settingsItem?.isVisible = stack.isEmpty()
        s.refresh()
    }

    fun openBrief(e: AgendaEvent) = push(BriefScreen(this, e))

    private fun hasMic() = ContextCompat.checkSelfPermission(this, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED

    /** Starts a recording for [event] (null = ad hoc), asking for permissions first if needed. */
    fun startRecording(event: EventRef?) {
        if (RecorderStatus.isRecording) {
            push(RecordingScreen(this))
            return
        }
        val needed = buildList {
            if (!hasMic()) add(Manifest.permission.RECORD_AUDIO)
            if (Build.VERSION.SDK_INT >= 33 &&
                ContextCompat.checkSelfPermission(this@MainActivity, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
            ) add(Manifest.permission.POST_NOTIFICATIONS)
        }
        if (!hasMic()) {
            pendingEvent = event
            pendingRecord = true
            permissions.launch(needed.toTypedArray())
            return
        }
        if (needed.isNotEmpty() && !askedNotifications) {
            // Notification permission is optional (recording works without it); ask once.
            askedNotifications = true
            pendingEvent = event
            pendingRecord = true
            permissions.launch(needed.toTypedArray())
            return
        }
        if (App.store(this).root.usableSpace < RecorderConfig.MIN_FREE_TO_START) {
            MaterialAlertDialogBuilder(this).setMessage("Not enough free space to record. Free at least 500 MB.").setPositiveButton("OK", null).show()
            return
        }
        RecorderService.start(this, event)
        RecorderStatus.eventSubject = event?.subject
        push(RecordingScreen(this))
    }

    fun requestCalendar() = permissions.launch(arrayOf(Manifest.permission.READ_CALENDAR))

    fun toast(s: String) = Toast.makeText(this, s, Toast.LENGTH_SHORT).show()

    private var askedNotifications = false

    companion object {
        private const val TAB_TODAY = 1
        private const val TAB_RECORDINGS = 2
        private const val TAB_MAC = 3
    }
}
