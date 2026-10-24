// The official firebase-storage and firebase-auth in production mode (no useEmulator), configured
// only with the google-services.json Aruma generated (copied here by ../run.sh). Storage reaches
// Aruma through the Gradle plugin dev.aruma.storage, at build time; Auth through aruma-redirect.
// The instrumented tests in src/androidTest run in the app's process.
//
// Gradle properties, all set by run.sh:
//   arumaUrl              ArumaURL in the manifest (Auth, and the run-time Storage fallback)
//   arumaStorageUrl       arumaStorage.url (written in upper case by run.sh: the plugin lowers it)
//   arumaStorageRedirect  ArumaStorageRedirect in the manifest (default true)
//   arumaE2eNoPlugin      build without dev.aruma.storage: aruma-redirect's launch check
//   arumaE2eExclude       exclude NetworkRequest from the instrumentation: the plugin's check
plugins {
    id("com.android.application")
    id("com.google.gms.google-services")
}

if (!providers.gradleProperty("arumaE2eNoPlugin").isPresent) {
    apply(plugin = "dev.aruma.storage")
    configure<dev.aruma.storage.ArumaStorageExtension> {
        url = providers.gradleProperty("arumaStorageUrl")
    }
}

android {
    // applicationId: the Android app Aruma's google-services.json was made for.
    namespace = "com.aruma.e2e"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.aruma.smoke"
        minSdk = 24
        targetSdk = 36
        // An App Bundle needs one.
        versionCode = 1
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        manifestPlaceholders["arumaUrl"] = providers.gradleProperty("arumaUrl").getOrElse("")
        manifestPlaceholders["arumaStorageRedirect"] = providers.gradleProperty("arumaStorageRedirect").getOrElse("true")
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

if (providers.gradleProperty("arumaE2eExclude").isPresent) {
    androidComponents {
        onVariants { it.instrumentation.excludes.add("com/google/firebase/storage/network/NetworkRequest") }
    }
}

// The APK a store would derive from the App Bundle: bundletool build-apks --mode=universal, the
// bundletool AGP itself uses. run.sh looks for the plugin's notice in it.
val bundletool = configurations.create("bundletool")
tasks.register<JavaExec>("universalApkFromBundle") {
    dependsOn("bundleDebug")
    val aab = layout.buildDirectory.file("outputs/bundle/debug/app-debug.aab")
    val apks = layout.buildDirectory.file("outputs/bundletool/app-debug.apks")
    val aapt2 = androidComponents.sdkComponents.sdkDirectory
        .map { it.file("build-tools/${android.buildToolsVersion}/aapt2") }
    inputs.file(aab)
    outputs.file(apks)
    classpath = bundletool
    mainClass = "com.android.tools.build.bundletool.BundleToolMain"
    argumentProviders.add {
        listOf("build-apks", "--mode=universal", "--overwrite", "--aapt2=${aapt2.get().asFile}",
            "--bundle=${aab.get().asFile}", "--output=${apks.get().asFile}")
    }
}

dependencies {
    bundletool("com.android.tools.build:bundletool:1.18.3")

    implementation(platform("com.google.firebase:firebase-bom:35.0.2"))
    implementation("com.google.firebase:firebase-auth")
    implementation("com.google.firebase:firebase-storage")
    implementation("dev.aruma:aruma-redirect:0.1.0")

    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation("junit:junit:4.13.2")
}
