package com.aruma.e2e;

import android.content.Context;
import android.net.Uri;
import android.util.Log;

import com.google.android.gms.tasks.Task;
import com.google.android.gms.tasks.Tasks;
import com.google.firebase.FirebaseApp;
import com.google.firebase.auth.FirebaseAuth;
import com.google.firebase.storage.FirebaseStorage;
import com.google.firebase.storage.ListResult;
import com.google.firebase.storage.StorageException;
import com.google.firebase.storage.StorageMetadata;
import com.google.firebase.storage.StorageReference;

import dev.aruma.redirect.ArumaUrls;

import org.json.JSONObject;

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
import java.util.List;
import java.util.Objects;
import java.util.TreeSet;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;

/**
 * What the app does with firebase-storage, as app code: in the release build R8 shrinks and
 * optimizes it with the rest of the app, firebase-storage and aruma-redirect included, and only
 * this class is kept for the instrumented tests to call (proguard-e2e.pro).
 *
 * <p>The operations are the twelve of the S0-B experiment (Android), now checked: each passes or fails, and the download URL is compared with the unified form
 * literally (ADR-022 §16), so a link on Google's host is a failure, not a warning.
 */
public final class Scenario {
    private static final String TAG = "ArumaE2E";

    /** One line per step, "✓ op  detail" or "✗ op  error", and how many passed. */
    public static final class Result {
        public final List<String> lines = new ArrayList<>();
        public int passed;

        public boolean allPassed() {
            return passed == lines.size();
        }

        @Override
        public String toString() {
            return String.join("\n", lines);
        }
    }

    private interface Op {
        String run() throws Exception;
    }

    private Scenario() {}

    /** The Storage base firebase-storage was built with: NetworkRequest.PROD_BASE_URL. */
    public static String prodBaseUrl() {
        return com.google.firebase.storage.network.NetworkRequest.PROD_BASE_URL.toString();
    }

    /** The plugin's notice, as the app's class loader finds it in the APK, or null. */
    public static String notice() throws Exception {
        InputStream in = Scenario.class.getClassLoader().getResourceAsStream("META-INF/aruma/firebase-storage-MODIFIED.txt");
        return in == null ? null : new String(readAll(in), StandardCharsets.UTF_8);
    }

    /**
     * The twelve operations, signed in anonymously, under e2e/{uid}/.
     *
     * @param storageUrl the origin the plugin must have written (lower case, no path).
     */
    public static Result twelveOperations(Context ctx, String storageUrl) throws Exception {
        Result res = new Result();
        FirebaseAuth auth = FirebaseAuth.getInstance();
        FirebaseStorage storage = storage();
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
        StorageReference[] refs = new StorageReference[4];
        String[] link = {null};

        step(res, "init", () -> {
            String uid = signIn(auth);
            StorageReference dir = storage.getReference().child("e2e/" + uid);
            refs[0] = dir;
            refs[1] = dir.child("small.txt");
            refs[2] = dir.child("big.bin");
            refs[3] = dir.child("file.bin");
            equal(storageUrl + "/v0", prodBaseUrl(), "PROD_BASE_URL");
            return "uid " + uid + ", bucket " + bucket;
        });
        if (refs[0] == null) {
            return res;
        }
        StorageReference dir = refs[0], smallRef = refs[1], bigRef = refs[2], fileRef = refs[3];

        step(res, "upload.small", () -> {
            long n = await(smallRef.putBytes(small, new StorageMetadata.Builder().setContentType("text/plain").build()))
                    .getBytesTransferred();
            equal((long) small.length, n, "bytes sent");
            return n + " bytes";
        });
        step(res, "upload.resumable3MB", () -> {
            long n = await(bigRef.putBytes(big)).getBytesTransferred();
            equal((long) big.length, n, "bytes sent");
            return n + " bytes";
        });
        step(res, "upload.putFile3MB", () -> {
            long n = await(fileRef.putFile(Uri.fromFile(bigFile))).getBytesTransferred();
            equal((long) big.length, n, "bytes sent");
            return n + " bytes";
        });
        step(res, "getMetadata", () -> {
            StorageMetadata m = await(smallRef.getMetadata());
            equal((long) small.length, m.getSizeBytes(), "size");
            equal("text/plain", m.getContentType(), "contentType");
            equal(bucket, m.getBucket(), "bucket");
            return "size " + m.getSizeBytes() + ", " + m.getContentType();
        });
        step(res, "getDownloadURL", () -> {
            link[0] = await(smallRef.getDownloadUrl()).toString();
            // The unified form, literally: the server's own token on the plugin's lower-case origin.
            String expected = storageUrl + "/v0/b/" + Uri.encode(bucket) + "/o/" + Uri.encode(smallRef.getPath().substring(1))
                    + "?alt=media&token=" + downloadToken(smallRef, auth, storageUrl);
            equal(expected, link[0], "download URL");
            // firebase-storage reads its own link back (Util.normalize on the lower-case host), and
            // the gs:// form from ArumaUrls.
            equal(smallRef.getPath(), storage.getReferenceFromUrl(link[0]).getPath(), "refFromURL(link)");
            equal(smallRef.getPath(), storage.getReferenceFromUrl(ArumaUrls.fromArumaUrl(link[0])).getPath(),
                    "refFromURL(fromArumaUrl(link))");
            return link[0].replaceAll("token=[^&]*", "token=<redacted>");
        });
        step(res, "download.getBytes", () -> {
            byte[] got = await(bigRef.getBytes(16 * 1024 * 1024));
            same(big, got, "getBytes");
            return got.length + " bytes, equal";
        });
        step(res, "download.getFile", () -> {
            File f = new File(ctx.getCacheDir(), "big-download.bin");
            await(bigRef.getFile(f));
            same(big, Files.readAllBytes(f.toPath()), "getFile");
            return f.length() + " bytes, equal";
        });
        step(res, "download.getStream", () -> {
            byte[] got = readAll(await(fileRef.getStream()).getStream());
            same(big, got, "getStream");
            return got.length + " bytes, equal";
        });
        step(res, "list", () -> {
            ListResult r = await(dir.listAll());
            TreeSet<String> names = new TreeSet<>();
            for (StorageReference item : r.getItems()) {
                names.add(item.getName());
            }
            equal(new TreeSet<>(Arrays.asList("big.bin", "file.bin", "small.txt")), names, "listAll");
            return names.toString();
        });
        step(res, "downloadURL.HttpURLConnection", () -> {
            // The link outside the library: platform HttpURLConnection, nothing in between.
            HttpURLConnection c = (HttpURLConnection) new URL(link[0]).openConnection();
            c.setConnectTimeout(5000);
            c.setReadTimeout(5000);
            equal(200, c.getResponseCode(), "status");
            same(small, readAll(c.getInputStream()), "body");
            return "200 from " + new URL(link[0]).getHost();
        });
        step(res, "delete", () -> {
            await(smallRef.delete());
            await(bigRef.delete());
            await(fileRef.delete());
            try {
                await(smallRef.getMetadata());
                throw new AssertionError("small.txt is still there");
            } catch (ExecutionException e) {
                if (!(e.getCause() instanceof StorageException)
                        || ((StorageException) e.getCause()).getErrorCode() != StorageException.ERROR_OBJECT_NOT_FOUND) {
                    throw new AssertionError("getMetadata after delete: " + e.getCause());
                }
            }
            return "3 objects, then getMetadata: object not found";
        });
        Log.i(TAG, "operations: " + res.passed + "/" + res.lines.size());
        return res;
    }

    /**
     * Names that need encoding, in every form of their URL: for each, the reference, its download
     * URL read back with getReferenceFromUrl, and that URL through ArumaUrls.fromArumaUrl must name
     * the same object and read the same bytes.
     */
    public static Result names(String storageUrl) throws Exception {
        Result res = new Result();
        FirebaseStorage storage = storage();
        String uid = signIn(FirebaseAuth.getInstance());
        for (String name : new String[] {"a b+é.txt", "صورة ١.png"}) {
            step(res, "name " + name, () -> {
                StorageReference ref = storage.getReference().child("e2e/" + uid + "/" + name);
                byte[] bytes = ("the object " + name).getBytes(StandardCharsets.UTF_8);
                await(ref.putBytes(bytes));
                try {
                    String link = await(ref.getDownloadUrl()).toString();
                    if (!link.startsWith(storageUrl + "/v0/b/")) {
                        throw new AssertionError("not on " + storageUrl + ": " + link);
                    }
                    String gs = ArumaUrls.fromArumaUrl(link);
                    StorageReference fromLink = storage.getReferenceFromUrl(link);
                    StorageReference fromGs = storage.getReferenceFromUrl(gs);
                    equal(ref.getPath(), fromLink.getPath(), "refFromURL(link)");
                    equal(ref.getPath(), fromGs.getPath(), "refFromURL(" + gs + ")");
                    for (StorageReference r : new StorageReference[] {ref, fromLink, fromGs}) {
                        same(bytes, await(r.getBytes(1024)), "bytes through " + r);
                    }
                    return "same object and bytes from the reference, the link and " + gs;
                } finally {
                    await(ref.delete());
                }
            });
        }
        return res;
    }

    private static FirebaseStorage storage() {
        FirebaseStorage storage = FirebaseStorage.getInstance();
        // A refused request is retried until these run out (defaults: 2 and 10 minutes).
        storage.setMaxOperationRetryTimeMillis(10_000);
        storage.setMaxUploadRetryTimeMillis(10_000);
        storage.setMaxDownloadRetryTimeMillis(10_000);
        return storage;
    }

    private static String signIn(FirebaseAuth auth) throws Exception {
        auth.signOut();
        return await(auth.signInAnonymously()).getUser().getUid();
    }

    private static void step(Result res, String op, Op fn) {
        String line;
        try {
            String detail = fn.run();
            res.passed++;
            line = "✓ " + op + (detail == null || detail.isEmpty() ? "" : "  " + detail);
        } catch (Throwable t) {
            Throwable c = t instanceof ExecutionException && t.getCause() != null ? t.getCause() : t;
            line = "✗ " + op + "  " + c.getClass().getSimpleName() + ": " + c.getMessage();
        }
        res.lines.add(line);
        Log.i(TAG, "op " + line);
    }

    /** downloadTokens from the object's /v0 metadata, as the SDK itself reads it, with the user's ID token. */
    private static String downloadToken(StorageReference ref, FirebaseAuth auth, String storageUrl) throws Exception {
        String idToken = await(auth.getCurrentUser().getIdToken(false)).getToken();
        HttpURLConnection c = (HttpURLConnection) new URL(storageUrl + "/v0/b/" + Uri.encode(ref.getBucket()) + "/o/"
                + Uri.encode(ref.getPath().substring(1))).openConnection();
        c.setRequestProperty("Authorization", "Firebase " + idToken);
        equal(200, c.getResponseCode(), "metadata status");
        String tokens = new JSONObject(new String(readAll(c.getInputStream()), StandardCharsets.UTF_8)).getString("downloadTokens");
        return tokens.split(",")[0];
    }

    private static void equal(Object want, Object got, String what) {
        if (!Objects.equals(want, got)) {
            throw new AssertionError(what + ": want <" + want + ">, got <" + got + ">");
        }
    }

    private static void same(byte[] want, byte[] got, String what) {
        if (!Arrays.equals(want, got)) {
            throw new AssertionError(what + ": " + got.length + " bytes differ from the " + want.length + " sent");
        }
    }

    private static <T> T await(Task<T> task) throws Exception {
        return Tasks.await(task, 90, TimeUnit.SECONDS);
    }

    static byte[] readAll(InputStream in) throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buf = new byte[64 * 1024];
        for (int n; (n = in.read(buf)) > 0; ) {
            out.write(buf, 0, n);
        }
        in.close();
        return out.toByteArray();
    }
}
