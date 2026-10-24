pluginManagement {
    // The Gradle plugin dev.aruma.storage from this repository, as a developer's build would take
    // the published one.
    includeBuild("../../../sdk/android/aruma-storage-gradle")
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
rootProject.name = "aruma-e2e-android-storage"
include(":app")

// The redirect library from this repository, in place of the published dev.aruma:aruma-redirect
// a developer's app declares.
includeBuild("../../../sdk/android/aruma-redirect") {
    dependencySubstitution {
        substitute(module("dev.aruma:aruma-redirect")).using(project(":aruma-redirect"))
    }
}
