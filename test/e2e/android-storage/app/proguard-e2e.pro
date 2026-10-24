# The instrumented tests call the app's Scenario; R8 shrinks everything else as it would in a
# store build, firebase-storage and aruma-redirect included (their consumer rules apply).
-keep class com.aruma.e2e.Scenario { public *; }
-keep class com.aruma.e2e.Scenario$Result { public *; }
# For the test runner only: AndroidJUnitRunner uses the Kotlin standard library, which the test
# APK leaves to the app it tests.
-keep class kotlin.** { *; }
