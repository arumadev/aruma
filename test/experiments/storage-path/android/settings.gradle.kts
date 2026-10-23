pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}
rootProject.name = "aruma-s0path-android"
include(":app")

// The redirect library from this repository (with the S0-B storage spike), in place of the
// published dev.aruma:aruma-redirect.
includeBuild("../../../../sdk/android/aruma-redirect") {
    dependencySubstitution {
        substitute(module("dev.aruma:aruma-redirect")).using(project(":aruma-redirect"))
    }
}
