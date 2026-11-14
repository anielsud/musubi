package com.musubi.phone.ui

import android.graphics.Bitmap
import android.os.SystemClock
import android.text.Editable
import android.text.InputFilter
import android.text.InputType
import android.text.TextWatcher
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.HorizontalScrollView
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.core.content.FileProvider
import com.google.android.material.chip.Chip
import com.google.android.material.chip.ChipGroup
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import com.musubi.phone.App
import com.musubi.phone.MainActivity
import com.musubi.phone.core.Details
import com.musubi.phone.core.DetailsEditor
import com.musubi.phone.core.Recording
import com.musubi.phone.core.SyncState
import com.musubi.phone.rec.PhotoPrep
import java.io.File
import java.time.Duration
import java.util.concurrent.Executors

/** One thread for details work (saves, photo preparation, thumbnails): never the UI's or the recorder's. */
private val detailsIo = Executors.newSingleThreadExecutor()

/**
 * Notes, People and Photos of a recording (docs/phone.md, "Recording details"), on the Recording
 * screen while it records and on a recording's screen afterwards. Edits go through
 * [DetailsEditor]: meta.json only, never the audio. Notes save themselves a moment after typing
 * stops, and when the screen is left.
 */
class DetailsPanel(private val a: MainActivity, private val recordingId: () -> String?) {
    private val editor get() = DetailsEditor(App.store(a))
    private lateinit var notes: TextInputEditText
    private lateinit var peopleHead: TextView
    private lateinit var people: ChipGroup
    private lateinit var photosHead: TextView
    private lateinit var photos: LinearLayout
    private lateinit var waiting: TextView
    private lateinit var body: LinearLayout
    private var boundTo: String? = null
    private var dirty = false
    private var lastEdit = 0L
    private var loading = false
    private val thumbs = HashMap<String, Bitmap>()

    val view: View by lazy { build() }

    private fun build(): View {
        val root = a.vbox()
        waiting = a.label("Notes, people and photos can be added once recording has started.", 13f, secondary = true)
        root.addView(waiting)
        body = a.vbox()
        body.visibility = View.GONE
        root.addView(body)

        body.addView(a.label("Notes", 16f, bold = true))
        val layout = TextInputLayout(a).apply {
            isCounterEnabled = true
            counterMaxLength = Details.MAX_NOTES
            hint = "What to remember about this meeting"
        }
        notes = TextInputEditText(layout.context).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or InputType.TYPE_TEXT_FLAG_CAP_SENTENCES
            minLines = 3
            gravity = Gravity.TOP or Gravity.START
            filters = arrayOf(InputFilter.LengthFilter(Details.MAX_NOTES))
            contentDescription = "Notes"
        }
        layout.addView(notes)
        notes.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
            override fun afterTextChanged(s: Editable?) {
                if (loading) return
                dirty = true
                lastEdit = SystemClock.elapsedRealtime()
            }
        })
        body.addView(layout)

        peopleHead = a.label("People", 16f, bold = true)
        body.addView(peopleHead.marginTop(a, 16))
        people = ChipGroup(a)
        body.addView(people)
        body.addView(a.button("Add person", outlined = true) { addPerson() })

        photosHead = a.label("Photos", 16f, bold = true)
        body.addView(photosHead.marginTop(a, 16))
        photos = LinearLayout(a).apply { orientation = LinearLayout.HORIZONTAL }
        body.addView(HorizontalScrollView(a).apply { addView(photos) })
        val row = LinearLayout(a).apply { orientation = LinearLayout.HORIZONTAL }
        row.addView(a.button("Take photo", outlined = true) { takePhoto() })
        row.addView(a.button("Choose photos", outlined = true) { choosePhotos() }.also {
            it.layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = a.dp(8) }
        })
        body.addView(row)
        return root
    }

    private fun current(): Recording? = recordingId()?.let { id -> runCatching { App.store(a).read(id) }.getOrNull() }

    /** Called every 250 ms: binds once the recording exists, and saves notes 800 ms after typing stops. */
    fun tick() {
        val id = recordingId()
        if (id != boundTo) bind(id)
        if (dirty && SystemClock.elapsedRealtime() - lastEdit > 800) flush()
    }

    private fun bind(id: String?) {
        if (id != null && current() == null) return // the recorder hasn't written meta.json yet
        boundTo = id
        waiting.visibility = if (id == null) View.VISIBLE else View.GONE
        body.visibility = if (id == null) View.GONE else View.VISIBLE
        refresh()
    }

    /** Saves the notes now (leaving the screen, the app pausing). */
    fun flush() {
        val id = boundTo ?: return
        if (!dirty) return
        dirty = false
        val text = notes.text?.toString() ?: ""
        detailsIo.execute {
            runCatching { editor.setNotes(id, text) }.onFailure { e -> a.runOnUiThread { a.toast(message(e)) } }
        }
    }

    /** Shows the recording's details as stored (notes only when nothing typed is waiting). */
    fun refresh() {
        val r = current() ?: return
        val d = r.meta.details
        val stored = App.store(a).readNotes(r.id)
        if (!dirty && notes.text?.toString() != stored) {
            loading = true
            notes.setText(stored)
            loading = false
        }
        peopleHead.text = "People (${d.attendees.size}/${Details.MAX_PEOPLE})"
        people.removeAllViews()
        for (p in d.attendees) {
            people.addView(Chip(a).apply {
                text = p.name
                isCloseIconVisible = true
                closeIconContentDescription = "Remove ${p.name}"
                setOnCloseIconClickListener { change { editor.removePerson(r.id, p.name) } }
            })
        }
        photosHead.text = "Photos (${d.photos.size}/${Details.MAX_PHOTOS})"
        photos.removeAllViews()
        for (p in d.photos) {
            val f = r.photoFile(p.name)
            val size = a.dp(88)
            val v: View = if (f.isFile) {
                ImageView(a).apply {
                    scaleType = ImageView.ScaleType.CENTER_CROP
                    contentDescription = "Photo ${p.name}"
                    thumbs[p.name]?.let { setImageBitmap(it) } ?: detailsIo.execute {
                        val bmp = runCatching { PhotoPrep.thumbnail(f, 256) }.getOrNull()
                        if (bmp != null) a.runOnUiThread { thumbs[p.name] = bmp; setImageBitmap(bmp) }
                    }
                    setOnClickListener { openPhoto(r.id, p.name, f) }
                }
            } else {
                // Deleted here once the Mac had it.
                a.label("On Mac", 12f, secondary = true).apply {
                    gravity = Gravity.CENTER
                    setOnClickListener { confirmDelete(r.id, p.name) }
                }
            }
            photos.addView(v, LinearLayout.LayoutParams(size, size).apply { marginEnd = a.dp(6) })
        }
    }

    private fun change(fn: () -> Unit) {
        detailsIo.execute {
            val err = runCatching { fn() }.exceptionOrNull()
            a.runOnUiThread {
                err?.let { a.toast(message(it)) }
                refresh()
            }
        }
    }

    private fun message(e: Throwable) = (e as? DetailsEditor.Refused)?.message ?: "Could not save: ${e.javaClass.simpleName}"

    private fun addPerson() {
        val r = current() ?: return
        val have = r.meta.details.attendees.map { it.name.lowercase() }.toSet()
        val ev = r.meta.event
        val fromEvent = ev?.let { e -> mergedEvents(a).firstOrNull { it.id == e.id }?.attendees }.orEmpty()
            .mapNotNull { Details.cleanName(it) }.distinct().filter { it.lowercase() !in have }
        val labels = fromEvent + "Type a name…"
        MaterialAlertDialogBuilder(a)
            .setTitle(if (fromEvent.isEmpty()) "Add person" else "Who was there?")
            .setItems(labels.toTypedArray()) { _, i ->
                if (i < fromEvent.size) change { editor.addPerson(r.id, fromEvent[i]) } else typeName(r.id)
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    private fun typeName(id: String) {
        val input = TextInputEditText(a).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_CAP_WORDS
            filters = arrayOf(InputFilter.LengthFilter(Details.MAX_NAME))
            hint = "Name"
        }
        val box = a.vbox(20).apply { addView(input) }
        MaterialAlertDialogBuilder(a)
            .setTitle("Add person")
            .setView(box)
            .setPositiveButton("Add") { _, _ -> change { editor.addPerson(id, input.text?.toString() ?: "") } }
            .setNegativeButton("Cancel", null)
            .show()
    }

    private fun roomForPhotos(): Int {
        val r = current() ?: return 0
        val left = Details.MAX_PHOTOS - r.meta.details.photos.size
        if (left <= 0) a.toast("At most ${Details.MAX_PHOTOS} photos")
        return left
    }

    private fun takePhoto() {
        val id = boundTo ?: return
        if (roomForPhotos() <= 0) return
        val f = editor.incoming(id)
        val uri = FileProvider.getUriForFile(a, MainActivity.FILES_AUTHORITY, f)
        a.takePhoto(uri) { ok ->
            if (!ok || !f.isFile || f.length() == 0L) {
                f.delete()
                return@takePhoto
            }
            addPrepared(id, listOf { f })
        }
    }

    private fun choosePhotos() {
        val id = boundTo ?: return
        val left = roomForPhotos()
        if (left <= 0) return
        a.pickPhotos { uris ->
            if (uris.size > left) a.toast("Only $left more photos can be added")
            addPrepared(id, uris.take(left).map { uri -> { editor.incoming(id).also { PhotoPrep.copyIn(a, uri, it) } } })
        }
    }

    /** Prepares and adds each photo on the details thread; [sources] produce the incoming files. */
    private fun addPrepared(id: String, sources: List<() -> File>) {
        detailsIo.execute {
            var failed: Throwable? = null
            for (src in sources) {
                var f: File? = null
                runCatching {
                    f = src()
                    PhotoPrep.prepare(f!!)
                    editor.addPhoto(id, f!!)
                }.onFailure { failed = it; f?.delete() }
            }
            a.runOnUiThread {
                failed?.let { a.toast(message(it)) }
                refresh()
            }
        }
    }

    private fun openPhoto(id: String, name: String, f: File) {
        val img = ImageView(a).apply { adjustViewBounds = true; contentDescription = "Photo $name" }
        detailsIo.execute {
            val bmp = runCatching { PhotoPrep.thumbnail(f, 1600) }.getOrNull()
            if (bmp != null) a.runOnUiThread { img.setImageBitmap(bmp) }
        }
        MaterialAlertDialogBuilder(a)
            .setView(img)
            .setPositiveButton("Close", null)
            .setNegativeButton("Delete") { _, _ -> confirmDelete(id, name) }
            .show()
    }

    private fun confirmDelete(id: String, name: String) {
        MaterialAlertDialogBuilder(a)
            .setMessage("Delete this photo from the recording? It is removed from the Mac at the next sync.")
            .setPositiveButton("Delete") { _, _ ->
                thumbs.remove(name)
                change { editor.deletePhoto(id, name) }
            }
            .setNegativeButton("Cancel", null)
            .show()
    }
}

/**
 * A recording after it stopped: its state, its event, and its notes, people and photos, which
 * can be edited at any time (the Mac takes in a later edit at its next sync).
 */
class RecordingDetailScreen(a: MainActivity, private val id: String) : Screen(a) {
    override val title = "Recording"
    private val panel = DetailsPanel(a) { id }
    private lateinit var head: TextView
    private lateinit var state: TextView
    private lateinit var changeEvent: View

    override fun build(): View {
        val root = a.vbox(16)
        head = a.label("", 16f, bold = true)
        root.addView(head)
        state = a.label("", 13f, secondary = true)
        root.addView(state)
        changeEvent = a.button("Change event", outlined = true) {
            val r = App.store(a).read(id) ?: return@button
            pickEvent(a, r.meta.start) { ev ->
                runCatching { App.store(a).updateMeta(id) { it.copy(event = ev) } }
                refresh()
            }
        }
        root.addView(changeEvent)
        if (App.store(a).liveTextFile(id) != null) root.addView(a.button("Live text", outlined = true) { a.push(LiveTextScreen(a, id)) })
        root.addView(panel.view.marginTop(a, 16))
        return ScrollView(a).apply {
            addView(root, ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        }
    }

    override fun refresh() {
        val r = App.store(a).read(id) ?: return a.pop()
        val m = r.meta
        val dur = m.end?.let { Fmt.elapsed(Duration.between(m.start, it).toMillis()) } ?: "…"
        head.text = (m.event?.subject?.ifBlank { "(no subject)" } ?: "Ad hoc") + "\n${Fmt.day(m.start)} ${Fmt.time(m.start)} · $dur"
        state.text = when (r.sync.state) {
            SyncState.IMPORTED -> if (m.details.rev > r.sync.detailsRev) "Transcribed · edits go to the Mac at the next sync" else "Transcribed"
            else -> r.sync.state.label
        }
        // The event can change until the Mac has the recording.
        changeEvent.visibility = if (r.sync.state == SyncState.ON_PHONE || r.sync.state == SyncState.UPLOADING) View.VISIBLE else View.GONE
        panel.tick()
        panel.refresh()
    }

    override fun tick() = panel.tick()
    override fun pause() = panel.flush()
}
