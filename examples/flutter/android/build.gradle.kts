// The Android Gradle plugin is the version sdk/android/aruma-redirect uses (settings.gradle.kts):
// one build, with its included builds, may load only one. dev.aruma.storage is applied by the app
// module when aruma.json turns Aruma on.
plugins {
    id("dev.aruma.storage") apply false
}

allprojects {
    repositories {
        google()
        mavenCentral()
    }
}

val newBuildDir: Directory =
    rootProject.layout.buildDirectory
        .dir("../../build")
        .get()
rootProject.layout.buildDirectory.value(newBuildDir)

subprojects {
    val newSubprojectBuildDir: Directory = newBuildDir.dir(project.name)
    project.layout.buildDirectory.value(newSubprojectBuildDir)
}
subprojects {
    project.evaluationDependsOn(":app")
}

tasks.register<Delete>("clean") {
    delete(rootProject.layout.buildDirectory)
}
