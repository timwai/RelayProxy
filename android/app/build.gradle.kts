plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

val relayAbi = providers.gradleProperty("relayAbi").orNull
val generatedHevLibs = layout.buildDirectory.dir("generated/hev-jniLibs")
val hevBuildScript = rootProject.projectDir.resolve("../scripts/build-hev-android.sh")
val hevBuildScriptPs1 = rootProject.projectDir.resolve("../scripts/build-hev-android.ps1")
val hevFlowOwnerPatch = rootProject.projectDir.resolve(
    "../third_party/hev-socks5-tunnel/relayproxy-flow-owner.patch"
)
val buildHevAndroidNative = tasks.register<Exec>("buildHevAndroidNative") {
    val windows = System.getProperty("os.name").lowercase().contains("windows")
    val script = if (windows) hevBuildScriptPs1 else hevBuildScript
    inputs.file(script)
    inputs.file(hevFlowOwnerPatch)
    outputs.dir(generatedHevLibs)
    environment("HEV_ANDROID_LIBS_OUT", generatedHevLibs.get().asFile.absolutePath)
    if (windows) {
        commandLine(
            "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass",
            "-File", script.absolutePath,
        )
    } else {
        commandLine("bash", script.absolutePath)
    }
}

android {
    namespace = "com.relayproxy.android"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.relayproxy.android"
        minSdk = 26
        targetSdk = 35
        versionCode = 2
        versionName = "0.2.0"

        ndk {
            if (!relayAbi.isNullOrBlank()) {
                abiFilters += relayAbi
            } else {
                // RelayProxy Android ships the two ARM ABIs used by the
                // supported phone builds. Filtering here also prevents the
                // gomobile AAR's development x86/x86_64 libraries from being
                // packaged and incorrectly treated as release artifacts.
                abiFilters += listOf("arm64-v8a", "armeabi-v7a")
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

    sourceSets.getByName("main").jniLibs.srcDir(generatedHevLibs)
}

tasks.named("preBuild").configure {
    dependsOn(buildHevAndroidNative)
}

listOf("Debug", "Release").forEach { variant ->
    val verifyPageAlignment = tasks.register("verify${variant}NativePageAlignment") {
        dependsOn("merge${variant}NativeLibs")
        doLast {
            val sdkRoot = System.getenv("ANDROID_SDK_ROOT")
                ?: System.getenv("ANDROID_HOME")
                ?: throw GradleException("ANDROID_SDK_ROOT is required to verify native libraries")
            val ndkRoot = System.getenv("ANDROID_NDK_HOME")
                ?: System.getenv("ANDROID_NDK_ROOT")
                ?: "$sdkRoot/ndk/27.2.12479018"
            val os = System.getProperty("os.name").lowercase()
            val hostTag = when {
                os.contains("windows") -> "windows-x86_64"
                os.contains("mac") || os.contains("darwin") -> "darwin-x86_64"
                else -> "linux-x86_64"
            }
            val readelf = file("$ndkRoot/toolchains/llvm/prebuilt/$hostTag/bin/llvm-readelf" +
                if (hostTag.startsWith("windows")) ".exe" else "")
            if (!readelf.isFile) throw GradleException("llvm-readelf not found: $readelf")

            val merged = layout.buildDirectory.dir(
                "intermediates/merged_native_libs/${variant.lowercase()}/merge${variant}NativeLibs/out/lib"
            ).get().asFile
            val libraries = merged.walkTopDown().filter { it.isFile && it.extension == "so" }.toList()
            if (libraries.isEmpty()) throw GradleException("No packaged native libraries found under $merged")
            libraries.forEach { library ->
                val process = ProcessBuilder(readelf.absolutePath, "-l", library.absolutePath)
                    .redirectErrorStream(true)
                    .start()
                val output = process.inputStream.bufferedReader().use { it.readText() }
                val exitCode = process.waitFor()
                if (exitCode != 0) throw GradleException("Could not inspect $library: $output")
                val loadSegments = output.lineSequence()
                    .map { it.trim().split(Regex("\\s+")) }
                    .filter { it.firstOrNull() == "LOAD" }
                    .toList()
                if (loadSegments.isEmpty() || loadSegments.any { it.lastOrNull() != "0x4000" }) {
                    throw GradleException("$library is not fully 16 KB aligned:\n$output")
                }
            }
            logger.lifecycle("Verified ${libraries.size} native libraries with 16 KB ELF alignment")
        }
    }
    tasks.matching { it.name == "package$variant" }.configureEach {
        dependsOn(verifyPageAlignment)
    }
}

kotlin {
    jvmToolchain(17)
}

dependencies {
    implementation(files("libs/mobilecore.aar"))
}
