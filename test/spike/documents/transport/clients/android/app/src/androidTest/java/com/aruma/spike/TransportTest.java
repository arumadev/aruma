package com.aruma.spike;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import android.content.Context;
import android.os.Bundle;
import android.os.SystemClock;
import android.util.Log;

import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;

import com.google.android.gms.tasks.Tasks;
import com.google.firebase.FirebaseApp;
import com.google.firebase.FirebaseOptions;
import com.google.firebase.firestore.DocumentReference;
import com.google.firebase.firestore.FirebaseFirestore;
import com.google.firebase.firestore.FirebaseFirestoreSettings;
import com.google.firebase.firestore.ListenerRegistration;
import com.google.firebase.firestore.MemoryCacheSettings;
import com.google.firebase.firestore.MetadataChanges;

import org.junit.AfterClass;
import org.junit.BeforeClass;
import org.junit.FixMethodOrder;
import org.junit.Test;
import org.junit.runner.RunWith;
import org.junit.runners.MethodSorters;

import java.util.Collections;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicReference;

/**
 * The official firebase-firestore against the transport spike server, set up in code with
 * FirebaseFirestoreSettings.setHost and setSslEnabled. ../run.sh runs it once per listener with
 * the arguments addr (host:port on the device's loopback, adb reverse), ssl, label, hold (seconds
 * the Listen stream must have been open when the later message arrives) and listenFor.
 *
 * <ul>
 *   <li>a_unary: a transaction, so BatchGetDocuments and Commit (mobile's only unary call);
 *   <li>b_write: DocumentReference.set, so the Write stream's handshake and one write;
 *   <li>c_listen: a snapshot listener held until the server's later document (65 s in).
 * </ul>
 */
@RunWith(AndroidJUnit4.class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
public class TransportTest {
    private static final String TAG = "ArumaSpike";
    private static FirebaseFirestore db;
    private static String label;

    private static String arg(String name, String fallback) {
        Bundle args = InstrumentationRegistry.getArguments();
        String v = args.getString(name);
        return v == null || v.isEmpty() ? fallback : v;
    }

    @BeforeClass
    public static void setUp() {
        NetworkGuard.install();
        Context ctx = InstrumentationRegistry.getInstrumentation().getTargetContext();
        label = arg("label", "spike");
        // Made-up identifiers: the SDK checks their presence, not their values, and nothing
        // reaches Google.
        FirebaseOptions options = new FirebaseOptions.Builder()
                .setProjectId("spike-project")
                .setApplicationId("1:1234567890:android:0123456789abcdef")
                .setApiKey("AIzaSyD-spike-made-up-key-0000000000000")
                .build();
        FirebaseApp app = FirebaseApp.initializeApp(ctx, options, label);
        db = FirebaseFirestore.getInstance(app);
        db.setFirestoreSettings(new FirebaseFirestoreSettings.Builder()
                .setHost(arg("addr", "127.0.0.1:9100"))
                .setSslEnabled(Boolean.parseBoolean(arg("ssl", "false")))
                .setLocalCacheSettings(MemoryCacheSettings.newBuilder().build())
                .build());
        Log.i(TAG, "label=" + label + " host=" + arg("addr", "") + " ssl=" + arg("ssl", "false"));
    }

    @AfterClass
    public static void guard() {
        Log.i(TAG, "RESULT " + label + " guard outside=" + NetworkGuard.outside());
        assertEquals("connections off loopback", Collections.emptyList(), NetworkGuard.outside());
    }

    @Test
    public void a_unary() throws Exception {
        DocumentReference doc = db.document("spike/tx");
        try {
            Tasks.await(db.runTransaction(tx -> {
                tx.get(doc);
                tx.set(doc, Collections.singletonMap("n", 1));
                return null;
            }), 60, TimeUnit.SECONDS);
            Log.i(TAG, "RESULT " + label + " unary PASS");
        } catch (Exception e) {
            Log.i(TAG, "RESULT " + label + " unary FAIL " + e);
            throw e;
        }
    }

    @Test
    public void b_write() throws Exception {
        try {
            Tasks.await(db.document("spike/write").set(Collections.singletonMap("n", 1)), 60, TimeUnit.SECONDS);
            Log.i(TAG, "RESULT " + label + " write PASS");
        } catch (Exception e) {
            Log.i(TAG, "RESULT " + label + " write FAIL " + e);
            throw e;
        }
    }

    @Test
    public void c_listen() throws Exception {
        long hold = Long.parseLong(arg("hold", "60"));
        long listenFor = Long.parseLong(arg("listenFor", "100"));
        CountDownLatch later = new CountDownLatch(1);
        AtomicBoolean firstFromServer = new AtomicBoolean();
        AtomicReference<String> problem = new AtomicReference<>();
        long begin = SystemClock.elapsedRealtime();
        ListenerRegistration reg = db.document("spike/doc").addSnapshotListener(MetadataChanges.INCLUDE, (snap, e) -> {
            if (e != null) {
                problem.set(e.toString());
                later.countDown();
                return;
            }
            boolean fromCache = snap.getMetadata().isFromCache();
            Log.i(TAG, "snapshot exists=" + snap.exists() + " fromCache=" + fromCache
                    + " after=" + (SystemClock.elapsedRealtime() - begin) / 1000 + "s");
            if (!fromCache && !snap.exists()) {
                firstFromServer.set(true);
            }
            if (!fromCache && snap.exists() && Boolean.TRUE.equals(snap.getBoolean("later"))) {
                later.countDown();
            }
        });
        boolean arrived = later.await(listenFor, TimeUnit.SECONDS);
        long held = (SystemClock.elapsedRealtime() - begin) / 1000;
        reg.remove();
        String result = !arrived ? "FAIL no later snapshot after " + held + "s"
                : problem.get() != null ? "FAIL " + problem.get()
                : !firstFromServer.get() ? "FAIL no first snapshot from the server"
                : held < hold ? "FAIL later snapshot after " + held + "s, before " + hold + "s"
                : "PASS held " + held + "s";
        Log.i(TAG, "RESULT " + label + " stream " + result);
        assertNull(problem.get());
        assertTrue(result, result.startsWith("PASS"));
    }
}
