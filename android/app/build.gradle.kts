plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

val relayAbi = providers.gradleProperty("relayAbi").orNull
val generatedBrandRes = layout.buildDirectory.dir("generated/relayproxyBrandRes")
val prepareBrandResources = tasks.register<Copy>("prepareBrandResources") {
    from(rootProject.file("../assets/brand/icon-256.png"))
    into(generatedBrandRes.map { it.dir("drawable-nodpi") })
    rename { "relayproxy_logo.png" }
}

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

    sourceSets {
        getByName("main").res.srcDir(generatedBrandRes)
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

tasks.configureEach {
    if (name.startsWith("merge") && name.endsWith("Resources")) {
        dependsOn(prepareBrandResources)
    }
}

kotlin {
    jvmToolchain(17)
}

dependencies {
    implementation(files("libs/mobilecore.aar"))
}
