// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

plugins {
    id("com.android.library")
    id("maven-publish")
}

android {
    namespace = "dev.aruma.redirect"
    compileSdk = 34

    defaultConfig {
        minSdk = 24
        consumerProguardFiles("consumer-rules.pro")
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_1_8
        targetCompatibility = JavaVersion.VERSION_1_8
    }

    testOptions {
        unitTests.isReturnDefaultValues = true
    }

    publishing {
        singleVariant("release") { withSourcesJar() }
    }
}

dependencies {
    testImplementation("junit:junit:4.13.2")
}

afterEvaluate {
    publishing {
        publications {
            create<MavenPublication>("release") {
                from(components["release"])
                groupId = "dev.aruma"
                artifactId = "aruma-redirect"
                version = "0.1.0"
            }
        }
    }
}
