// The official google-services plugin reads app/google-services.json, copied from the file
// Aruma generated (see test/smoke/run.sh), and turns it into Android resources.
plugins {
    id("com.android.application")
    id("com.google.gms.google-services")
}

android {
    namespace = "com.aruma.smoke"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.aruma.smoke"
        minSdk = 24
        targetSdk = 36
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    testOptions {
        unitTests {
            isIncludeAndroidResources = true
            all {
                it.systemProperty("aruma.expected", rootProject.file("../.generated/expected.json").absolutePath)
                // Robolectric's Android framework jars go next to the Gradle caches, not ~/.m2.
                it.systemProperty("maven.repo.local", gradle.gradleUserHomeDir.resolve("robolectric").absolutePath)
                it.maxHeapSize = "1g"
            }
        }
    }
}

dependencies {
    implementation(platform("com.google.firebase:firebase-bom:35.0.2"))
    implementation("com.google.firebase:firebase-installations")

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.robolectric:robolectric:4.17")
    testImplementation("androidx.test:core:1.7.0")
    testImplementation("org.json:json:20250517")
}
