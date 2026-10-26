// The Android Gradle plugin of the other e2e apps (test/e2e/android-*), so the CI caches serve both.
plugins {
    id("com.android.application") version "9.0.1" apply false
}
