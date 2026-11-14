package com.musubi.phone.rec

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Matrix
import android.media.ExifInterface
import android.net.Uri
import com.musubi.phone.core.Details
import com.musubi.phone.core.DetailsEditor
import java.io.File
import java.io.FileOutputStream
import java.io.IOException

/**
 * Turns a photo on its way in (camera output, a picked image) into what a recording keeps: a JPEG of
 * at most 15 MB. A JPEG within the limit is kept as it is; anything else (a bigger JPEG, HEIC, PNG)
 * is decoded at a size that fits, turned upright and re-encoded.
 */
object PhotoPrep {
    /** The most a picked file may be before it is even looked at. */
    private const val MAX_INPUT_BYTES = 200L * 1024 * 1024
    private const val MAX_EDGE = 4096

    /** Copies a picked image into [dst] (an incoming file), at most [MAX_INPUT_BYTES]. */
    fun copyIn(ctx: Context, uri: Uri, dst: File) {
        val input = ctx.contentResolver.openInputStream(uri) ?: throw IOException("the photo could not be read")
        input.use { inp ->
            FileOutputStream(dst).use { out ->
                val buf = ByteArray(64 * 1024)
                var total = 0L
                while (true) {
                    val n = inp.read(buf)
                    if (n < 0) break
                    total += n
                    if (total > MAX_INPUT_BYTES) throw IOException("the photo is too large")
                    out.write(buf, 0, n)
                }
            }
        }
    }

    /** Makes [f] (an incoming file) a JPEG within the limit, in place. */
    fun prepare(f: File) {
        if (f.length() in 1..Details.MAX_PHOTO_BYTES && DetailsEditor.isJpeg(f)) return
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeFile(f.path, bounds)
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) throw IOException("not an image")
        var sample = 1
        while (maxOf(bounds.outWidth, bounds.outHeight) / sample > MAX_EDGE) sample *= 2
        var bmp = BitmapFactory.decodeFile(f.path, BitmapFactory.Options().apply { inSampleSize = sample })
            ?: throw IOException("not an image")
        bmp = upright(bmp, f)
        var quality = 90
        val tmp = File(f.parentFile, f.name + ".jpg")
        try {
            while (true) {
                FileOutputStream(tmp).use { bmp.compress(Bitmap.CompressFormat.JPEG, quality, it) }
                if (tmp.length() <= Details.MAX_PHOTO_BYTES) break
                if (quality > 60) quality -= 15 else {
                    bmp = Bitmap.createScaledBitmap(bmp, bmp.width / 2, bmp.height / 2, true)
                }
            }
            if (!tmp.renameTo(f)) throw IOException("could not keep the photo")
        } finally {
            tmp.delete()
            bmp.recycle()
        }
    }

    /** A small, upright copy for the screen. */
    fun thumbnail(f: File, edge: Int): Bitmap? {
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeFile(f.path, bounds)
        if (bounds.outWidth <= 0) return null
        var sample = 1
        while (maxOf(bounds.outWidth, bounds.outHeight) / (sample * 2) >= edge) sample *= 2
        val bmp = BitmapFactory.decodeFile(f.path, BitmapFactory.Options().apply { inSampleSize = sample }) ?: return null
        return upright(bmp, f)
    }

    private fun upright(bmp: Bitmap, f: File): Bitmap {
        val deg = when (runCatching { ExifInterface(f.path).getAttributeInt(ExifInterface.TAG_ORIENTATION, ExifInterface.ORIENTATION_NORMAL) }.getOrDefault(0)) {
            ExifInterface.ORIENTATION_ROTATE_90 -> 90f
            ExifInterface.ORIENTATION_ROTATE_180 -> 180f
            ExifInterface.ORIENTATION_ROTATE_270 -> 270f
            else -> 0f
        }
        if (deg == 0f) return bmp
        val out = Bitmap.createBitmap(bmp, 0, 0, bmp.width, bmp.height, Matrix().apply { postRotate(deg) }, true)
        if (out != bmp) bmp.recycle()
        return out
    }
}
