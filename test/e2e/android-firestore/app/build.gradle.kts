// The official firebase-firestore and firebase-auth, configured only with the google-services.json
// Aruma generated (copied here by ../run.sh); the Firestore host is set in code to the server the
// run started (a Firestore SDK has no other way to leave Google), and aruma-redirect sends Auth to
// Aruma. The instrumented tests in src/androidTest run in the app's process.
plugins {
    id("com.android.application")
    id("com.google.gms.google-services")
}

android {
    // applicationId: the Android app Aruma's google-services.json was made for.
    namespace = "com.aruma.e2e"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.aruma.smoke"
        minSdk = 24
        targetSdk = 36
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        // The Aruma server (-ParumaUrl, set by run.sh), written into the manifest as ArumaURL.
        manifestPlaceholders["arumaUrl"] = providers.gradleProperty("arumaUrl").getOrElse("")
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation(platform("com.google.firebase:firebase-bom:35.0.2"))
    implementation("com.google.firebase:firebase-auth")
    implementation("com.google.firebase:firebase-firestore")
    implementation("dev.aruma:aruma-redirect:0.1.0")

    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation("junit:junit:4.13.2")
}
