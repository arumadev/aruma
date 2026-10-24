"""Adapts the platform folder `flutter create` made for the e2e app (run.sh) to the production path.

    prepare.py android <app> <arumaURL> [<ndk dir>]
    prepare.py ios <app> <arumaURL>

Android (ADR-022 §16.2): the Gradle plugin dev.aruma.storage and aruma-redirect from this
repository, ArumaURL and ArumaStorageRedirect=false in the manifest (the plugin is mandatory),
cleartext to loopback, and the application id Aruma's config was made for.

iOS (ADR-022 §16.3): ArumaURL and ArumaStorageRedirect=false in Info.plist (read by the patched
FirebaseStorage and by ArumaRedirect), local networking for http to this machine, iOS 15 (as
FlutterFire needs), and the bundle id Aruma's config was made for. The mirror is set by run.sh.

Each edit must find its anchor exactly once, or this fails: a new `flutter create` template is
noticed, not half-patched.
"""

import os
import plistlib
import re
import sys

APP_ID = "com.aruma.smoke"  # test/smoke/genconfig's Android package and iOS bundle id


def edit(path, old, new, count=1):
    s = open(path, encoding="utf-8").read()
    n = s.count(old)
    if n != count:
        sys.exit(f"prepare.py: {path}: {old!r} found {n} times, want {count}")
    open(path, "w", encoding="utf-8").write(s.replace(old, new))


def android(app, aruma_url, ndk):
    root = os.path.relpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "../../.."), os.path.join(app, "android"))
    a = os.path.join(app, "android")
    edit(f"{a}/settings.gradle.kts", '    includeBuild("$flutterSdkPath/packages/flutter_tools/gradle")\n',
         '    includeBuild("$flutterSdkPath/packages/flutter_tools/gradle")\n'
         '    // dev.aruma.storage from this repository, as an app would take the published plugin.\n'
         f'    includeBuild("{root}/sdk/android/aruma-storage-gradle")\n')
    edit(f"{a}/settings.gradle.kts", 'include(":app")\n',
         'include(":app")\n\n'
         '// aruma-redirect from this repository, in place of the published dev.aruma:aruma-redirect.\n'
         f'includeBuild("{root}/sdk/android/aruma-redirect") {{\n'
         '    dependencySubstitution {\n'
         '        substitute(module("dev.aruma:aruma-redirect")).using(project(":aruma-redirect"))\n'
         '    }\n'
         '}\n')
    g = f"{a}/app/build.gradle.kts"
    edit(g, '    id("dev.flutter.flutter-gradle-plugin")\n',
         '    id("dev.flutter.flutter-gradle-plugin")\n    id("dev.aruma.storage")\n')
    edit(g, 'flutter {\n', 'arumaStorage {\n'
         '    // run.sh: the server, as ORG_GRADLE_PROJECT_arumaStorageUrl.\n'
         '    url = providers.gradleProperty("arumaStorageUrl")\n'
         '}\n\nflutter {\n')
    s = open(g, encoding="utf-8").read()
    s, n = re.subn(r'applicationId = "[^"]*"', f'applicationId = "{APP_ID}"', s)
    if n != 1:
        sys.exit(f"prepare.py: {g}: applicationId found {n} times")
    if ndk:
        # The Flutter Gradle plugin wants an NDK (to strip libflutter.so) and downloads one into
        # ANDROID_HOME when it is missing; use one already on disk.
        s, n = re.subn(r'\n\s*ndkVersion = flutter\.ndkVersion', f'\n    ndkPath = "{ndk}"', s)
        if n != 1:
            sys.exit(f"prepare.py: {g}: ndkVersion found {n} times")
    open(g, "w", encoding="utf-8").write(s)
    # This machine has 8 GB; the template asks Gradle for 8. dev.aruma.storage instruments every
    # class in scope ALL, Flutter's engine jars included, and 1.5 GB was not enough for that.
    p = f"{a}/gradle.properties"
    s = open(p, encoding="utf-8").read()
    s, n = re.subn(r"org\.gradle\.jvmargs=.*", "org.gradle.jvmargs=-Xmx2560m -XX:MaxMetaspaceSize=768m -Dfile.encoding=UTF-8\norg.gradle.workers.max=2", s)
    if n != 1:
        sys.exit(f"prepare.py: {p}: org.gradle.jvmargs found {n} times")
    open(p, "w", encoding="utf-8").write(s)
    m = f"{a}/app/src/main/AndroidManifest.xml"
    # The release manifest has no INTERNET permission in the template (debug and profile add it).
    edit(m, "<manifest xmlns:android=\"http://schemas.android.com/apk/res/android\">\n",
         "<manifest xmlns:android=\"http://schemas.android.com/apk/res/android\">\n"
         "    <uses-permission android:name=\"android.permission.INTERNET\" />\n")
    edit(m, "    <application\n", "    <application\n        android:usesCleartextTraffic=\"true\"\n")
    edit(m, "    </application>\n",
         "        <!-- aruma-redirect: Auth goes to Aruma. Storage needs no redirect: the plugin -->\n"
         "        <!-- built it for Aruma, and with ArumaStorageRedirect=false it is mandatory. -->\n"
         f"        <meta-data android:name=\"ArumaURL\" android:value=\"{aruma_url}\" />\n"
         "        <meta-data android:name=\"ArumaStorageRedirect\" android:value=\"false\" />\n"
         "    </application>\n")


def ios(app, aruma_url):
    i = os.path.join(app, "ios")
    p = f"{i}/Runner/Info.plist"
    with open(p, "rb") as f:
        info = plistlib.load(f)
    info["ArumaURL"] = aruma_url
    info["ArumaStorageRedirect"] = False
    info["NSAppTransportSecurity"] = {"NSAllowsLocalNetworking": True}
    with open(p, "wb") as f:
        plistlib.dump(info, f)
    x = f"{i}/Runner.xcodeproj/project.pbxproj"
    s = open(x, encoding="utf-8").read()
    s, n = re.subn(r"IPHONEOS_DEPLOYMENT_TARGET = 13\.0;", "IPHONEOS_DEPLOYMENT_TARGET = 15.0;", s)
    if n == 0:
        sys.exit(f"prepare.py: {x}: no IPHONEOS_DEPLOYMENT_TARGET = 13.0")
    s, n = re.subn(r"PRODUCT_BUNDLE_IDENTIFIER = [A-Za-z0-9.]+\.RunnerTests;", f"PRODUCT_BUNDLE_IDENTIFIER = {APP_ID}.RunnerTests;", s)
    s, m = re.subn(r"PRODUCT_BUNDLE_IDENTIFIER = (?![A-Za-z0-9.]+\.RunnerTests;)[A-Za-z0-9.]+;", f"PRODUCT_BUNDLE_IDENTIFIER = {APP_ID};", s)
    if m != 3:
        sys.exit(f"prepare.py: {x}: Runner's PRODUCT_BUNDLE_IDENTIFIER found {m} times, want 3")
    open(x, "w", encoding="utf-8").write(s)


if __name__ == "__main__":
    if len(sys.argv) < 4 or sys.argv[1] not in ("android", "ios"):
        sys.exit(__doc__)
    if sys.argv[1] == "android":
        android(sys.argv[2], sys.argv[3], sys.argv[4] if len(sys.argv) > 4 else "")
    else:
        ios(sys.argv[2], sys.argv[3])
