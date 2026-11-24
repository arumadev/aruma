pluginManagement {
    val flutterSdkPath =
        run {
            val properties = java.util.Properties()
            file("local.properties").inputStream().use { properties.load(it) }
            val flutterSdkPath = properties.getProperty("flutter.sdk")
            require(flutterSdkPath != null) { "flutter.sdk not set in local.properties" }
            flutterSdkPath
        }

    includeBuild("$flutterSdkPath/packages/flutter_tools/gradle")
    // The Gradle plugin dev.aruma.storage from this repository, as an app takes the published one
    // (docs/guides/mobile-apps.md; outside this repository: mavenLocal() and the plugin's version).
    includeBuild("../../../sdk/android/aruma-storage-gradle")

    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

plugins {
    id("dev.flutter.flutter-plugin-loader") version "1.0.0"
    id("com.android.application") version "9.0.1" apply false
    id("org.jetbrains.kotlin.android") version "2.3.20" apply false
}

include(":app")

// aruma-redirect from this repository, in place of the published dev.aruma:aruma-redirect the app
// declares (outside this repository: publishToMavenLocal and mavenLocal(), as the guide says).
includeBuild("../../../sdk/android/aruma-redirect") {
    dependencySubstitution {
        substitute(module("dev.aruma:aruma-redirect")).using(project(":aruma-redirect"))
    }
}
