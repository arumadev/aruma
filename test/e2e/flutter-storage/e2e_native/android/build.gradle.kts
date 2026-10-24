// The e2e plugin's Android half. aruma-redirect comes from this repository: the app's
// settings.gradle.kts substitutes the published coordinates with sdk/android/aruma-redirect.
plugins {
    id("com.android.library")
}

group = "dev.aruma.e2e"
version = "0.1.0"

android {
    namespace = "dev.aruma.e2e"
    compileSdk = 36

    defaultConfig {
        minSdk = 24
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation("dev.aruma:aruma-redirect:0.1.0")
}
