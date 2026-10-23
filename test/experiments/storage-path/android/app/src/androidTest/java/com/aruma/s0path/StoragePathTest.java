package com.aruma.s0path;

import android.content.Context;
import android.net.Uri;
import android.os.Bundle;
import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import com.google.android.gms.tasks.Task;
import com.google.android.gms.tasks.Tasks;
import com.google.firebase.FirebaseApp;
import com.google.firebase.FirebaseOptions;
import com.google.firebase.storage.FirebaseStorage;
import com.google.firebase.storage.StorageMetadata;
import com.google.firebase.storage.StorageReference;
import dev.aruma.redirect.ArumaRedirect;
import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.Arrays;
import java.util.concurrent.TimeUnit;
import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.Test;
import org.junit.runner.RunWith;

/**
 * S0-B: one mode per instrumentation run (a fresh process), chosen by the {@code mode} argument:
 *
 * <ul>
 *   <li>{@code emulator}: {@code useEmulator("127.0.0.1", port)}.
 *   <li>{@code prod-asis}: production mode, aruma-redirect installed as it is (Auth hosts only).
 *   <li>{@code prod-spike}: production mode, aruma-redirect with the storage spike, Aruma on http.
 *   <li>{@code prod-spike-https}: the same with Aruma on https (the stub's throwaway CA).
 * </ul>
 *
 * <p>Every operation is recorded, not asserted; the results go back as one JSON instrumentation
 * status ({@code s0path=…}), which ../android.sh collects. The device guard (iptables) blocks
 * anything that leaves loopback; {@link NetworkGuard} records it in process.
 */
@RunWith(AndroidJUnit4.class)
public class StoragePathTest {
    private final JSONArray results = new JSONArray();

    private interface Op {
        Object run() throws Exception;
    }

    private void step(String op, Op fn) throws Exception {
        JSONObject r = new JSONObject().put("op", op);
        try {
            Object detail = fn.run();
            r.put("ok", true).put("detail", detail == null ? "" : String.valueOf(detail));
        } catch (Throwable t) {
            Throwable c = t.getCause() != null ? t.getCause() : t;
            String msg = c.getClass().getSimpleName() + ": " + c.getMessage();
            if (c.getCause() != null) {
                msg += " / " + c.getCause().getClass().getSimpleName() + ": " + c.getCause().getMessage();
            }
            r.put("ok", false).put("detail", msg.length() > 240 ? msg.substring(0, 240) : msg);
        }
        results.put(r);
    }

    private static <T> T await(Task<T> task) throws Exception {
        return Tasks.await(task, 90, TimeUnit.SECONDS);
    }

    private static byte[] readAll(InputStream in) throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buf = new byte[64 * 1024];
        for (int n; (n = in.read(buf)) > 0; ) {
            out.write(buf, 0, n);
        }
        in.close();
        return out.toByteArray();
    }

    private static String redact(String url) {
        return url.replaceAll("token=[^&]*", "token=<redacted>");
    }

    @Test
    public void storagePath() throws Exception {
        Bundle args = InstrumentationRegistry.getArguments();
        String mode = args.getString("mode", "prod-spike");
        String stubHttp = args.getString("stubHttp", "http://127.0.0.1:8820");
        String stubHttps = args.getString("stubHttps", "https://localhost:8821");
        NetworkGuard.install();
        Context ctx = ApplicationProvider.getApplicationContext();

        if (!mode.equals("emulator")) {
            ArumaRedirect.install(mode.endsWith("https") ? stubHttps : stubHttp, false, true);
            ArumaRedirect.setStorageSpike(mode.startsWith("prod-spike"));
        }
        FirebaseOptions options = new FirebaseOptions.Builder()
                .setApiKey("AIzaS0pathNotARealKey")
                .setApplicationId("1:1:android:s0path")
                .setProjectId("s0path")
                .setStorageBucket("s0path.appspot.com")
                .build();
        FirebaseApp app = FirebaseApp.initializeApp(ctx, options, "s0path");
        FirebaseStorage storage = FirebaseStorage.getInstance(app);
        if (mode.equals("emulator")) {
            storage.useEmulator("127.0.0.1", Uri.parse(stubHttp).getPort());
        }
        // A blocked request is retried until these run out (defaults: 2 and 10 minutes).
        storage.setMaxOperationRetryTimeMillis(4000);
        storage.setMaxUploadRetryTimeMillis(4000);
        storage.setMaxDownloadRetryTimeMillis(4000);
        results.put(new JSONObject().put("op", "init").put("ok", true).put("detail", "redirect=" + ArumaRedirect.baseUrl()));

        byte[] small = "hello aruma\n".getBytes(StandardCharsets.UTF_8);
        byte[] big = new byte[3 * 1024 * 1024 + 17];
        Arrays.fill(big, (byte) 7);
        File bigFile = new File(ctx.getCacheDir(), "big-upload.bin");
        try (FileOutputStream out = new FileOutputStream(bigFile)) {
            out.write(big);
        }
        StorageReference root = storage.getReference();
        StorageReference smallRef = root.child("s0/small.txt");
        StorageReference bigRef = root.child("s0/big.bin");
        StorageReference fileRef = root.child("s0/file.bin");
        String[] url = {null};

        step("upload.small", () -> await(smallRef.putBytes(small,
                new StorageMetadata.Builder().setContentType("text/plain").build())).getBytesTransferred());
        step("upload.resumable3MB", () -> await(bigRef.putBytes(big)).getBytesTransferred());
        step("upload.putFile3MB", () -> await(fileRef.putFile(Uri.fromFile(bigFile))).getBytesTransferred());
        step("getMetadata", () -> "size=" + await(smallRef.getMetadata()).getSizeBytes());
        step("getDownloadURL", () -> redact((url[0] = await(smallRef.getDownloadUrl()).toString())));
        step("download.getBytes", () -> {
            byte[] got = await(bigRef.getBytes(16 * 1024 * 1024));
            if (!Arrays.equals(got, big)) throw new IllegalStateException("got " + got.length + " bytes");
            return got.length + " bytes";
        });
        step("download.getFile", () -> {
            File f = new File(ctx.getCacheDir(), "big-download.bin");
            await(bigRef.getFile(f));
            return f.length() + " bytes";
        });
        step("download.getStream", () -> readAll(await(fileRef.getStream()).getStream()).length + " bytes");
        step("list", () -> await(root.child("s0").listAll()).getItems().size() + " items");
        step("downloadURL.HttpURLConnection", () -> {
            HttpURLConnection c = (HttpURLConnection) new URL(url[0]).openConnection();
            c.setConnectTimeout(5000);
            c.setReadTimeout(5000);
            int code = c.getResponseCode();
            return code + " " + readAll(c.getInputStream()).length + " bytes from " + new URL(url[0]).getHost();
        });
        step("delete", () -> {
            await(smallRef.delete());
            await(bigRef.delete());
            await(fileRef.delete());
            return "";
        });

        JSONObject out = new JSONObject()
                .put("scenario", "android-" + mode)
                .put("results", results)
                .put("blocked", new JSONArray(NetworkGuard.outside()));
        Bundle status = new Bundle();
        status.putString("s0path", out.toString());
        InstrumentationRegistry.getInstrumentation().sendStatus(0, status);
    }
}
