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

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }
}

kotlin {
    jvmToolchain(17)
}

dependencies {
    implementation(files("libs/mobilecore.aar"))
}
