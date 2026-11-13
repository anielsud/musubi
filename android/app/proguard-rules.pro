# sherpa-onnx's native code finds its Kotlin config classes, fields and
# callbacks by name (JNI FindClass/GetFieldID), so R8 must not rename or
# remove them: live text would fail to start in release builds only.
-keep class com.k2fsa.sherpa.onnx.** { *; }
