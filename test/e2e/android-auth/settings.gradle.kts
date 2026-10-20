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
rootProject.name = "aruma-e2e-android-auth"
include(":app")

// The redirect library from this repository, in place of the published dev.aruma:aruma-redirect
// a developer's app declares.
includeBuild("../../../sdk/android/aruma-redirect") {
    dependencySubstitution {
        substitute(module("dev.aruma:aruma-redirect")).using(project(":aruma-redirect"))
    }
}
