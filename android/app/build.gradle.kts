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
        versionCode = 2
        versionName = "0.2.0"
        // Smoke tests only (-PupdatesMockBase=https://10.0.2.2:8443 -PupdatesMockPin=<cert SHA-256>): a local mock of
        // the GitHub releases API. Empty in every real build; release.sh sets both empty.
        fun mockProp(name: String) = (project.findProperty(name) as String? ?: "").also {
            require(Regex("^[A-Za-z0-9:/._\\[\\]-]*$").matches(it)) { "$name: unexpected characters" }
        }
        buildConfigField("String", "UPDATES_MOCK_BASE", "\"${mockProp("updatesMockBase")}\"")
        buildConfigField("String", "UPDATES_MOCK_PIN", "\"${mockProp("updatesMockPin")}\"")
    }

    // Release key and passwords come only from the environment (android/release.sh reads them from the Keychain).
    val releaseKeystore = System.getenv("MUSUBI_RELEASE_KEYSTORE")
    signingConfigs {
        if (releaseKeystore != null) {
            create("release") {
                storeFile = file(releaseKeystore)
                storePassword = System.getenv("MUSUBI_RELEASE_STORE_PASSWORD")
                keyAlias = System.getenv("MUSUBI_RELEASE_KEY_ALIAS") ?: "musubi-phone"
                keyPassword = System.getenv("MUSUBI_RELEASE_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        debug {
            // Debug-only: `./gradlew assembleDebug -PchunkSeconds=20` rotates chunks quickly for smoke tests.
            val chunkSeconds = (project.findProperty("chunkSeconds") as String?)?.toLongOrNull() ?: 0L
            buildConfigField("long", "CHUNK_SECONDS_OVERRIDE", "${chunkSeconds}L")
        }
        release {
            isMinifyEnabled = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"))
            if (releaseKeystore != null) signingConfig = signingConfigs.getByName("release")
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

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.7")
    implementation("androidx.work:work-runtime-ktx:2.9.1")
    implementation("com.google.android.material:material:1.12.0")

    testImplementation("junit:junit:4.13.2")
    // android.jar only has stubs of org.json; unit tests need the real classes (public domain).
    testImplementation("org.json:json:20240303")
}
