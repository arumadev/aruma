// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The Gradle plugin dev.aruma.storage (ADR-022 §16.2): it points the official firebase-storage at
// an Aruma server at build time, in the app's own build.
plugins {
    `java-gradle-plugin`
    `maven-publish`
}

group = "dev.aruma"
version = "0.1.0"

// The Android Gradle plugin 9 runs on Java 17 or later; the plugin is built for 17.
java {
    withSourcesJar()
}

dependencies {
    // The app's build supplies the Android Gradle plugin, and with it the ASM it instruments with.
    compileOnly("com.android.tools.build:gradle-api:9.0.1")
    implementation("org.ow2.asm:asm:9.8")

    testImplementation("junit:junit:4.13.2")
}

gradlePlugin {
    plugins {
        create("arumaStorage") {
            id = "dev.aruma.storage"
            implementationClass = "dev.aruma.storage.ArumaStoragePlugin"
            displayName = "Aruma Storage"
            description = "Points the official firebase-storage for Android at an Aruma server at build time."
        }
    }
}

tasks.jar {
    manifest {
        attributes("Implementation-Title" to "dev.aruma.storage", "Implementation-Version" to project.version)
    }
}

tasks.withType<JavaCompile>().configureEach {
    options.release = 17
    options.compilerArgs.addAll(listOf("-Xlint:all", "-Werror"))
}
