plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

val relayAbi = providers.gradleProperty("relayAbi").orNull

android {
    namespace = "com.relayproxy.android"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.relayproxy.android"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"

        if (!relayAbi.isNullOrBlank()) {
            ndk {
                abiFilters += relayAbi
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }

    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }
}

dependencies {
    implementation(files("libs/mobilecore.aar"))
}
