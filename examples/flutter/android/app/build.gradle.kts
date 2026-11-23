import groovy.json.JsonSlurper

plugins {
    id("com.android.application")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

// The one Aruma setting, in aruma.json at the root of the Flutter project, which the Dart code reads
// too. With "useAruma": false this is a plain FlutterFire app: no plugin, no library, no ArumaURL.
val aruma = JsonSlurper().parse(rootProject.file("../aruma.json")) as Map<*, *>
val useAruma = aruma["useAruma"] == true
val arumaUrl = if (useAruma) (aruma["arumaUrl"] as String).trim().trimEnd('/') else ""

if (useAruma) {
    // Cloud Storage: the SDK's host constant becomes Aruma's when the app is built, so
    // getDownloadURL() gives Aruma links (docs/guides/mobile-apps.md, aruma-storage-gradle).
    // http is taken for loopback and, in debug builds, the emulator's 10.0.2.2.
    apply(plugin = "dev.aruma.storage")
    configure<dev.aruma.storage.ArumaStorageExtension> {
        url = arumaUrl
    }
}

android {
    namespace = "dev.aruma.examples.team_board"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = flutter.ndkVersion

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    defaultConfig {
        // The package name you register as the Android app in your Aruma (or Firebase) project.
        applicationId = "dev.aruma.examples.team_board"
        // You can update the following values to match your application needs.
        // For more information, see: https://flutter.dev/to/review-gradle-config.
        minSdk = flutter.minSdkVersion
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName
        // AndroidManifest.xml: ArumaURL (aruma-redirect, Auth) and ArumaStorageRedirect=false, so
        // that a build without dev.aruma.storage stops at launch instead of reaching Google.
        manifestPlaceholders["arumaUrl"] = arumaUrl
        manifestPlaceholders["arumaStorageRedirect"] = if (useAruma) "false" else "true"
    }

    buildTypes {
        release {
            // TODO: Add your own signing config for the release build.
            // Signing with the debug keys for now, so `flutter run --release` works.
            signingConfig = signingConfigs.getByName("debug")
        }
    }
}

kotlin {
    compilerOptions {
        jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
    }
}

flutter {
    source = "../.."
}

dependencies {
    // Auth: Firebase Auth's requests go to ArumaURL (docs/guides/mobile-apps.md, aruma-redirect).
    if (useAruma) implementation("dev.aruma:aruma-redirect:0.1.0")
}
