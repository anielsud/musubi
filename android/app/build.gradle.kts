plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.musubi.phone"
    compileSdk = 35
    buildToolsVersion = "35.0.0"

    defaultConfig {
        applicationId = "com.musubi.phone"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    buildTypes {
        debug {
            // Debug-only: `./gradlew assembleDebug -PchunkSeconds=20` rotates chunks quickly for smoke tests.
            val chunkSeconds = (project.findProperty("chunkSeconds") as String?)?.toLongOrNull() ?: 0L
            buildConfigField("long", "CHUNK_SECONDS_OVERRIDE", "${chunkSeconds}L")
        }
        release {
            isMinifyEnabled = false
            buildConfigField("long", "CHUNK_SECONDS_OVERRIDE", "0L")
        }
    }

    buildFeatures {
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }

    testOptions {
        unitTests.isReturnDefaultValues = false
    }
}

// Live text engine: sherpa-onnx built from pinned, hash-checked source (sherpa-onnx/build.sh),
// arm64-v8a only. Output goes to the gitignored sherpa-onnx/out/.
val sherpaOnnxDir = rootProject.file("sherpa-onnx")
val sherpaOnnx by tasks.registering(Exec::class) {
    inputs.file(File(sherpaOnnxDir, "build.sh"))
    outputs.dir(File(sherpaOnnxDir, "out"))
    environment("ANDROID_SDK_ROOT", android.sdkDirectory.path)
    commandLine("bash", File(sherpaOnnxDir, "build.sh").path)
}
android.sourceSets["main"].jniLibs.srcDir(File(sherpaOnnxDir, "out/jniLibs"))
android.sourceSets["main"].java.srcDir(File(sherpaOnnxDir, "out/kotlin"))
tasks.named("preBuild") { dependsOn(sherpaOnnx) }

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.7")
    implementation("androidx.work:work-runtime-ktx:2.9.1")
    implementation("com.google.android.material:material:1.12.0")

    testImplementation("junit:junit:4.13.2")
    // android.jar only has stubs of org.json; unit tests need the real classes (public domain).
    testImplementation("org.json:json:20240303")
    androidTestImplementation("androidx.test:runner:1.6.2")
}
