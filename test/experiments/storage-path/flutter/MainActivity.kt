// Copied by ../flutter.sh over the generated MainActivity. It installs aruma-redirect (with the
// S0-B storage spike in mode prod-spike) before the Flutter engine starts, and tells Dart the mode.
package com.aruma.s0path_flutter

import android.os.Bundle
import dev.aruma.redirect.ArumaRedirect
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterActivity() {
    private val mode get() = intent.getStringExtra("mode") ?: "prod-spike"
    private val stub get() = intent.getStringExtra("stub") ?: "http://127.0.0.1:8820"

    override fun onCreate(savedInstanceState: Bundle?) {
        if (mode != "emulator" && ArumaRedirect.baseUrl() == null) {
            ArumaRedirect.install(stub, false, true)
            ArumaRedirect.setStorageSpike(mode == "prod-spike")
        }
        super.onCreate(savedInstanceState)
    }

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "s0path").setMethodCallHandler { call, result ->
            when (call.method) {
                "mode" -> result.success(mode)
                "stub" -> result.success(stub)
                else -> result.notImplemented()
            }
        }
    }
}
