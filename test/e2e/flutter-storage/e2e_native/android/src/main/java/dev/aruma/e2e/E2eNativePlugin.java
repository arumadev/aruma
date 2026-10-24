package dev.aruma.e2e;

import android.os.Handler;
import android.os.Looper;

import io.flutter.embedding.engine.plugins.FlutterPlugin;
import io.flutter.plugin.common.MethodCall;
import io.flutter.plugin.common.MethodChannel;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * The e2e app's native half on Android: installs the in-process network guard when the engine
 * starts (before Dart asks firebase-storage for anything), and answers the "aruma_e2e" channel:
 * {@code info} (what dev.aruma.storage wrote into the app), {@code guard} and {@code selfTest}.
 */
public final class E2eNativePlugin implements FlutterPlugin, MethodChannel.MethodCallHandler {
    private static final String NOTICE = "META-INF/aruma/firebase-storage-MODIFIED.txt";
    private MethodChannel channel;

    @Override
    public void onAttachedToEngine(FlutterPluginBinding binding) {
        NetworkGuard.install();
        channel = new MethodChannel(binding.getBinaryMessenger(), "aruma_e2e");
        channel.setMethodCallHandler(this);
    }

    @Override
    public void onDetachedFromEngine(FlutterPluginBinding binding) {
        channel.setMethodCallHandler(null);
    }

    @Override
    public void onMethodCall(MethodCall call, MethodChannel.Result result) {
        switch (call.method) {
            case "info": {
                Map<String, Object> info = new HashMap<>();
                info.put("platform", "android");
                try {
                    // Public in firebase-storage, and kept under R8 by aruma-redirect's consumer rules.
                    info.put("prodBaseUrl", String.valueOf(Class.forName("com.google.firebase.storage.network.NetworkRequest")
                            .getField("PROD_BASE_URL").get(null)));
                    info.put("notice", notice());
                } catch (Exception e) {
                    result.error("info", e.toString(), null);
                    return;
                }
                result.success(info);
                return;
            }
            case "guard": {
                List<String> outside = NetworkGuard.outside();
                Map<String, Object> g = new HashMap<>();
                g.put("outside", outside);
                g.put("summary", NetworkGuard.loopbackCount() + " HttpURLConnection connections, all to loopback"
                        + (outside.isEmpty() ? "" : ", and " + outside.size() + " outside"));
                result.success(g);
                return;
            }
            case "selfTest":
                selfTest(result);
                return;
            default:
                result.notImplemented();
        }
    }

    /**
     * The guard's self-test: an HttpURLConnection to TEST-NET-1 (RFC 5737, never routed) must be
     * recorded; on the device iptables then refuses it (run.sh counts that too).
     */
    private static void selfTest(MethodChannel.Result result) {
        Handler main = new Handler(Looper.getMainLooper());
        new Thread(() -> {
            int before = NetworkGuard.outside().size();
            String outcome;
            try {
                HttpURLConnection c = (HttpURLConnection) new URL("http://192.0.2.1/").openConnection();
                c.setConnectTimeout(3000);
                c.setReadTimeout(3000);
                outcome = "connected (" + c.getResponseCode() + ")";
            } catch (Exception e) {
                outcome = e.getClass().getSimpleName();
            }
            List<String> outside = NetworkGuard.outside();
            if (outside.size() != before + 1 || !outside.get(before).equals("http://192.0.2.1")) {
                String msg = "the guard did not record http://192.0.2.1: " + outside;
                main.post(() -> result.error("selfTest", msg, null));
                return;
            }
            NetworkGuard.forget(1);
            String detail = "recorded http://192.0.2.1, then " + outcome;
            main.post(() -> result.success(detail));
        }).start();
    }

    private static String notice() throws Exception {
        InputStream in = E2eNativePlugin.class.getClassLoader().getResourceAsStream(NOTICE);
        if (in == null) {
            return null;
        }
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buf = new byte[8192];
        for (int n; (n = in.read(buf)) > 0; ) {
            out.write(buf, 0, n);
        }
        in.close();
        return out.toString(StandardCharsets.UTF_8.name());
    }
}
