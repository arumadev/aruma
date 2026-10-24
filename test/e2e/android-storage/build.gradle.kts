// The Android Gradle plugin must be the version sdk/android/aruma-redirect uses: one build (with
// its included builds) may load only one.
plugins {
    id("com.android.application") version "9.0.1" apply false
    id("com.google.gms.google-services") version "4.5.0" apply false
    id("dev.aruma.storage") apply false
}
