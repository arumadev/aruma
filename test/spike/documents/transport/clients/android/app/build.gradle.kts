// The official firebase-firestore against the transport spike server (../../../server), configured
// in code (FirebaseOptions and FirebaseFirestoreSettings.setHost), with no google-services.json.
// The instrumented tests in src/androidTest run in the app's process; ../run.sh drives them.
plugins {
    id("com.android.application")
}

android {
    namespace = "com.aruma.spike"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.aruma.spike"
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
    implementation("com.google.firebase:firebase-firestore")

    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation("junit:junit:4.13.2")
}
