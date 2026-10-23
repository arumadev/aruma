// S0-B: the official firebase-storage (BoM 35.0.2, firebase-storage 23.0.0) in production mode and
// in emulator mode, with aruma-redirect installed from the test (no ArumaURL in the manifest), so
// each instrumentation run picks its mode. The app has no code: the instrumented test runs in its
// process. The FirebaseApp is built from FirebaseOptions in the test (no google-services.json).
plugins {
    id("com.android.application")
}

android {
    namespace = "com.aruma.s0path"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.aruma.s0path"
        minSdk = 24
        targetSdk = 36
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation(platform("com.google.firebase:firebase-bom:35.0.2"))
    implementation("com.google.firebase:firebase-storage")
    implementation("dev.aruma:aruma-redirect:0.1.0")

    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation("junit:junit:4.13.2")
}
