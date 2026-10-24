package com.aruma.e2e;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import android.content.Context;
import android.net.Uri;
import android.os.Bundle;
import android.util.Log;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;

import com.google.android.gms.tasks.Task;
import com.google.android.gms.tasks.Tasks;
import com.google.firebase.FirebaseApp;
import com.google.firebase.auth.FirebaseAuth;
import com.google.firebase.auth.FirebaseUser;
import com.google.firebase.storage.FirebaseStorage;
import com.google.firebase.storage.ListResult;
import com.google.firebase.storage.StorageException;
import com.google.firebase.storage.StorageMetadata;
import com.google.firebase.storage.StorageReference;
import com.google.firebase.storage.network.NetworkRequest;

import dev.aruma.redirect.ArumaRedirect;
import dev.aruma.redirect.ArumaUrls;

import org.json.JSONObject;
import org.junit.After;
import org.junit.AfterClass;
import org.junit.Before;
import org.junit.BeforeClass;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.TreeSet;
import java.util.concurrent.TimeUnit;

/**
 * The official firebase-storage for Android in production mode, pointed at Aruma at build time by
 * the Gradle plugin dev.aruma.storage (no useEmulator, no run-time redirect of Storage), signed in
 * with firebase-auth through aruma-redirect, against a real server under storage.rules, with every
 * other connection recorded here and rejected on the device (NetworkGuard, run.sh).
 *
 * <p>The operations are the twelve of the S0-B experiment (docs/research/storage-production-path.md,
 * Android), now asserted: each passes or fails, and a download URL is checked against the unified
 * form literally (ADR-022 §16), so a link on Google's host is a failure, not a warning.
 *
 * <p>run.sh passes arumaUrl (ArumaURL, Auth) and storageUrl (the origin the plugin was given in
 * upper case, here in the lower case the plugin must write).
 */
@RunWith(AndroidJUnit4.class)
public class StorageE2ETest {
    private static final String TAG = "ArumaE2E";
    private static final String NOTICE = "META-INF/aruma/firebase-storage-MODIFIED.txt";
    private static final Bundle args = InstrumentationRegistry.getArguments();
    private static final String arumaUrl = args.getString("arumaUrl", "");
    private static final String storageUrl = args.getString("storageUrl", "");

    private int outsideBefore;

    @BeforeClass
    public static void installGuard() {
        NetworkGuard.install();
    }

    @AfterClass
    public static void summary() {
        List<String> outside = NetworkGuard.outside();
        Log.i(TAG, "guard: " + outside.size() + " connections outside the device " + outside);
    }

    @Before
    public void setUp() {
        assertFalse("run through test/e2e/android-storage/run.sh (no arumaUrl argument)", arumaUrl.isEmpty());
        assertFalse("run through test/e2e/android-storage/run.sh (no storageUrl argument)", storageUrl.isEmpty());
        outsideBefore = NetworkGuard.outside().size();
    }

    @After
    public void tearDown() {
        List<String> outside = NetworkGuard.outside();
        assertEquals("connections leaving the device", Collections.emptyList(), outside.subList(outsideBefore, outside.size()));
    }

    /** The build-time path is the one in place, and its notice ships in the APK. */
    @Test
    public void builtWithThePlugin() throws Exception {
        assertEquals(arumaUrl, ArumaRedirect.baseUrl());
        // The plugin wrote the host in lower case and nothing else: no :443, no prefix.
        assertEquals(storageUrl + "/v0", NetworkRequest.PROD_BASE_URL.toString());
        InputStream in = FirebaseStorage.class.getClassLoader().getResourceAsStream(NOTICE);
        assertNotNull(NOTICE + " is not in the APK", in);
        String notice = new String(readAll(in), StandardCharsets.UTF_8);
        assertTrue(notice, notice.contains("modified copy of firebase-storage"));
        assertTrue(notice, notice.contains("\"" + storageUrl + "/v0\""));
    }

    // The twelve operations.

    private final List<String> results = new ArrayList<>();
    private int passed;

    private interface Op {
        String run() throws Exception;
    }

    private void step(String op, Op fn) {
        String line;
        try {
            String detail = fn.run();
            passed++;
            line = "✓ " + op + (detail == null || detail.isEmpty() ? "" : "  " + detail);
        } catch (Throwable t) {
            Throwable c = t.getCause() != null && t instanceof java.util.concurrent.ExecutionException ? t.getCause() : t;
            line = "✗ " + op + "  " + c.getClass().getSimpleName() + ": " + c.getMessage();
        }
        results.add(line);
        Log.i(TAG, "op " + line);
    }

    @Test
    public void twelveOperations() throws Exception {
        Context ctx = ApplicationProvider.getApplicationContext();
        FirebaseAuth auth = FirebaseAuth.getInstance();
        FirebaseStorage storage = FirebaseStorage.getInstance();
        // A refused request is retried until these run out (defaults: 2 and 10 minutes).
        storage.setMaxOperationRetryTimeMillis(10_000);
        storage.setMaxUploadRetryTimeMillis(10_000);
        storage.setMaxDownloadRetryTimeMillis(10_000);
        String bucket = FirebaseApp.getInstance().getOptions().getStorageBucket();

        byte[] small = "hello aruma\n".getBytes(StandardCharsets.UTF_8);
        byte[] big = new byte[3 * 1024 * 1024 + 17];
        for (int i = 0; i < big.length; i++) {
            big[i] = (byte) (i * 31 + 7);
        }
        File bigFile = new File(ctx.getCacheDir(), "big-upload.bin");
        try (FileOutputStream out = new FileOutputStream(bigFile)) {
            out.write(big);
        }
        String[] uid = {null};
        StorageReference[] refs = new StorageReference[4];
        String[] link = {null};

        step("init", () -> {
            auth.signOut();
            FirebaseUser user = await(auth.signInAnonymously()).getUser();
            uid[0] = user.getUid();
            StorageReference dir = storage.getReference().child("e2e/" + uid[0]);
            refs[0] = dir;
            refs[1] = dir.child("small.txt");
            refs[2] = dir.child("big.bin");
            refs[3] = dir.child("file.bin");
            assertEquals(storageUrl + "/v0", NetworkRequest.PROD_BASE_URL.toString());
            return "uid " + uid[0] + ", bucket " + bucket;
        });
        StorageReference dir = refs[0], smallRef = refs[1], bigRef = refs[2], fileRef = refs[3];
        if (dir == null) {
            fail("sign-in failed: " + results);
        }

        step("upload.small", () -> {
            long n = await(smallRef.putBytes(small, new StorageMetadata.Builder().setContentType("text/plain").build()))
                    .getBytesTransferred();
            assertEquals(small.length, n);
            return n + " bytes";
        });
        step("upload.resumable3MB", () -> {
            long n = await(bigRef.putBytes(big)).getBytesTransferred();
            assertEquals(big.length, n);
            return n + " bytes";
        });
        step("upload.putFile3MB", () -> {
            long n = await(fileRef.putFile(Uri.fromFile(bigFile))).getBytesTransferred();
            assertEquals(big.length, n);
            return n + " bytes";
        });
        step("getMetadata", () -> {
            StorageMetadata m = await(smallRef.getMetadata());
            assertEquals(small.length, m.getSizeBytes());
            assertEquals("text/plain", m.getContentType());
            assertEquals(bucket, m.getBucket());
            return "size " + m.getSizeBytes() + ", " + m.getContentType();
        });
        step("getDownloadURL", () -> {
            link[0] = await(smallRef.getDownloadUrl()).toString();
            // The unified form, literally: the server's own token on the plugin's lower-case origin.
            String expected = storageUrl + "/v0/b/" + Uri.encode(bucket) + "/o/" + Uri.encode(smallRef.getPath().substring(1))
                    + "?alt=media&token=" + downloadToken(smallRef, auth);
            assertEquals(expected, link[0]);
            // firebase-storage reads its own link back (Util.normalize on the lower-case host),
            // and so does every other form through ArumaUrls.
            assertEquals(smallRef.getPath(), storage.getReferenceFromUrl(link[0]).getPath());
            assertEquals(smallRef.getPath(), storage.getReferenceFromUrl(ArumaUrls.fromArumaUrl(link[0])).getPath());
            return redact(link[0]);
        });
        step("download.getBytes", () -> {
            byte[] got = await(bigRef.getBytes(16 * 1024 * 1024));
            assertArrayEquals(big, got);
            return got.length + " bytes, equal";
        });
        step("download.getFile", () -> {
            File f = new File(ctx.getCacheDir(), "big-download.bin");
            await(bigRef.getFile(f));
            assertArrayEquals(big, Files.readAllBytes(f.toPath()));
            return f.length() + " bytes, equal";
        });
        step("download.getStream", () -> {
            byte[] got = readAll(await(fileRef.getStream()).getStream());
            assertArrayEquals(big, got);
            return got.length + " bytes, equal";
        });
        step("list", () -> {
            ListResult r = await(dir.listAll());
            TreeSet<String> names = new TreeSet<>();
            for (StorageReference item : r.getItems()) {
                names.add(item.getName());
            }
            assertEquals(new TreeSet<>(Arrays.asList("big.bin", "file.bin", "small.txt")), names);
            return names.toString();
        });
        step("downloadURL.HttpURLConnection", () -> {
            // The link outside the library: platform HttpURLConnection, nothing in between.
            HttpURLConnection c = (HttpURLConnection) new URL(link[0]).openConnection();
            c.setConnectTimeout(5000);
            c.setReadTimeout(5000);
            assertEquals(200, c.getResponseCode());
            assertArrayEquals(small, readAll(c.getInputStream()));
            return "200 from " + new URL(link[0]).getHost();
        });
        step("delete", () -> {
            await(smallRef.delete());
            await(bigRef.delete());
            await(fileRef.delete());
            try {
                await(smallRef.getMetadata());
                fail("small.txt is still there");
            } catch (java.util.concurrent.ExecutionException e) {
                assertTrue(String.valueOf(e.getCause()), e.getCause() instanceof StorageException);
                assertEquals(StorageException.ERROR_OBJECT_NOT_FOUND, ((StorageException) e.getCause()).getErrorCode());
            }
            return "3 objects, then getMetadata: object not found";
        });

        Log.i(TAG, "operations: " + passed + "/" + results.size());
        assertEquals("operations\n" + String.join("\n", results), 12, results.size());
        assertEquals("operations\n" + String.join("\n", results), results.size(), passed);
    }

    /** downloadTokens from the object's /v0 metadata, as the SDK itself reads it, with the user's ID token. */
    private static String downloadToken(StorageReference ref, FirebaseAuth auth) throws Exception {
        String idToken = await(auth.getCurrentUser().getIdToken(false)).getToken();
        HttpURLConnection c = (HttpURLConnection) new URL(storageUrl + "/v0/b/" + Uri.encode(ref.getBucket()) + "/o/"
                + Uri.encode(ref.getPath().substring(1))).openConnection();
        c.setRequestProperty("Authorization", "Firebase " + idToken);
        assertEquals(200, c.getResponseCode());
        String tokens = new JSONObject(new String(readAll(c.getInputStream()), StandardCharsets.UTF_8)).getString("downloadTokens");
        return tokens.split(",")[0];
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
}
