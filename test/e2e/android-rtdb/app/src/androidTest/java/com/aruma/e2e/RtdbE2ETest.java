package com.aruma.e2e;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import android.content.ComponentName;
import android.content.Context;
import android.content.Intent;
import android.content.ServiceConnection;
import android.os.Bundle;
import android.os.IBinder;
import android.os.Parcel;
import android.os.Process;
import android.os.ResultReceiver;
import android.util.Log;

import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;

import com.google.android.gms.tasks.Task;
import com.google.android.gms.tasks.TaskCompletionSource;
import com.google.android.gms.tasks.Tasks;
import com.google.firebase.FirebaseApp;
import com.google.firebase.auth.FirebaseAuth;
import com.google.firebase.database.ChildEventListener;
import com.google.firebase.database.DataSnapshot;
import com.google.firebase.database.DatabaseError;
import com.google.firebase.database.DatabaseReference;
import com.google.firebase.database.FirebaseDatabase;
import com.google.firebase.database.MutableData;
import com.google.firebase.database.Query;
import com.google.firebase.database.ServerValue;
import com.google.firebase.database.Transaction;
import com.google.firebase.database.ValueEventListener;

import dev.aruma.redirect.ArumaRedirect;

import org.json.JSONObject;
import org.json.JSONTokener;
import org.junit.After;
import org.junit.AfterClass;
import org.junit.Before;
import org.junit.BeforeClass;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.Callable;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.function.Predicate;

/**
 * The official firebase-database for Android in production mode, configured only with the
 * google-services.json Aruma generated (its firebase_url is the database), signed in with
 * firebase-auth through aruma-redirect, against a real server under the rules of
 * test/e2e/rtdb-rules.json, with every other connection recorded here and rejected on the device
 * (NetworkGuard, run.sh). The second party is the database's REST API with an admin credential,
 * and for presence a client in another process. The scenarios follow test/e2e/web-rtdb.
 *
 * <p>run.sh passes the server as instrumentation arguments: arumaUrl, projectId, cliToken and
 * switchUrl (the server's off switch, genconfig -switch).
 */
@RunWith(AndroidJUnit4.class)
public class RtdbE2ETest {
    private static final String TAG = "ArumaE2E";
    private static final Bundle args = InstrumentationRegistry.getArguments();
    private static final String arumaUrl = args.getString("arumaUrl", "");
    private static final String projectId = args.getString("projectId", "");
    private static final String ns = projectId + "-default-rtdb";
    /** Tells this run's paths and addresses apart from an earlier run's on the same server. */
    private static final String run = Long.toString(System.currentTimeMillis(), 36);

    private FirebaseAuth auth;
    private FirebaseDatabase db;
    private int outsideBefore;
    private final List<Runnable> cleanup = new ArrayList<>();

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
        assertFalse("run through test/e2e/android-rtdb/run.sh (no arumaUrl argument)", arumaUrl.isEmpty());
        auth = FirebaseAuth.getInstance();
        auth.signOut();
        db = FirebaseDatabase.getInstance();
        db.goOnline();
        outsideBefore = NetworkGuard.outside().size();
    }

    @After
    public void tearDown() {
        for (Runnable r : cleanup) {
            r.run();
        }
        List<String> outside = NetworkGuard.outside();
        assertEquals("connections leaving the device", Collections.emptyList(), outside.subList(outsideBefore, outside.size()));
    }

    // 1. Sign-in, and the database SDK in production mode on the generated URL.

    @Test
    public void productionModeOnTheGeneratedDatabaseUrl() throws Exception {
        assertEquals(arumaUrl, ArumaRedirect.baseUrl());
        assertEquals(arumaUrl + "/?ns=" + ns, FirebaseApp.getInstance().getOptions().getDatabaseUrl());
        // The SDK took host and namespace from that URL: the root reference is on Aruma.
        assertEquals(arumaUrl, db.getReference().toString());
        for (String name : new String[] {
                "com.google.firebase.analytics.FirebaseAnalytics",
                "com.google.firebase.installations.FirebaseInstallations",
                "com.google.firebase.messaging.FirebaseMessaging"}) {
            try {
                Class.forName(name);
                fail(name + " is linked in");
            } catch (ClassNotFoundException expected) {
                // not in the app
            }
        }
        String uid = await(auth.createUserWithEmailAndPassword("signin-android-" + run + "@example.com", "secret1")).getUser().getUid();
        assertEquals(28, uid.length());
        // A write under the user's own path: the connection carries Aruma's ID token.
        await(db.getReference("users").child(uid).child("profile").setValue(map("name", "سارة")));
        assertEquals("{\"name\":\"سارة\"}", rest("GET", "users/" + uid + "/profile", null).body);
    }

    // 2. Rules: own data yes, another user's no.

    @Test
    public void rulesAllowOwnDataAndDenyOthers() throws Exception {
        String uid = signInAnonymously();
        DatabaseReference mine = db.getReference("users").child(uid);
        await(mine.setValue(map("note", "mine")));
        assertEquals(map("note", "mine"), await(mine.get()).getValue());

        String other = "other-" + run;
        assertEquals(200, rest("PUT", "users/" + other, "{\"note\":\"theirs\"}").status);
        DatabaseReference theirs = db.getReference("users").child(other);
        assertEquals(Integer.valueOf(DatabaseError.PERMISSION_DENIED), writeError(theirs.child("note"), "overwritten"));
        Recorder r = listen(theirs);
        r.await("cancelled", 10_000, e -> e.size() == 1 && e.get(0).kind.equals("cancelled"));
        assertEquals(DatabaseError.PERMISSION_DENIED, r.events().get(0).error.getCode());
        try {
            await(theirs.get());
            fail("get() of another user's data succeeded");
        } catch (ExecutionException e) {
            assertTrue(String.valueOf(e.getCause()), String.valueOf(e.getCause().getMessage()).contains("ermission"));
        }
        assertEquals("{\"note\":\"theirs\"}", rest("GET", "users/" + other, null).body);

        // Signed out, not even the own data.
        auth.signOut();
        eventually("own data denied when signed out", 10_000, () -> writeError(mine.child("note"), "again") != null);
    }

    // 3. A write by the other party reaches this client's listener, in order, with the server's time.

    @Test
    public void writesReachTheOtherPartyInOrderWithServerTimestamps() throws Exception {
        String uid = signInAnonymously();
        DatabaseReference room = db.getReference("rooms").child("android-" + run).child("messages");
        Recorder added = listenChildren(room.orderByChild("ts"));
        Recorder all = listen(room.orderByChild("ts"));

        long t0 = System.currentTimeMillis();
        List<String> keys = new ArrayList<>();
        for (int i = 1; i <= 3; i++) {
            DatabaseReference m = room.push();
            await(m.setValue(map("from", uid, "text", "device" + i, "ts", ServerValue.TIMESTAMP)));
            keys.add(m.getKey());
            Rest posted = rest("POST", "rooms/android-" + run + "/messages", "{\"from\":\"admin\",\"text\":\"admin" + i + "\",\"ts\":{\".sv\":\"timestamp\"}}");
            assertEquals(200, posted.status);
            String key = ((JSONObject) posted.json()).getString("name");
            keys.add(key);
            // The admin's message reaches this client's listener before its next write.
            added.await("admin" + i, 10_000, e -> e.stream().anyMatch(x -> x.kind.equals("added") && key.equals(x.key)));
        }
        long t1 = System.currentTimeMillis();

        List<String> addedKeys = new ArrayList<>();
        for (Recorder.Event e : added.events()) {
            if (e.kind.equals("added")) {
                addedKeys.add(e.key);
            }
        }
        assertEquals(keys, addedKeys);

        // The final value: children in the order of ts, each ts the server's time as REST reads it.
        JSONObject stored = (JSONObject) rest("GET", "rooms/android-" + run + "/messages", null).json();
        all.await("the server's timestamps", 10_000, e -> {
            Recorder.Event last = e.isEmpty() ? null : e.get(e.size() - 1);
            if (last == null || last.keys.size() != 6) {
                return false;
            }
            for (String k : keys) {
                Object ts = ((Map<?, ?>) ((Map<?, ?>) last.value).get(k)).get("ts");
                if (ts == null || ((Number) ts).longValue() != stored.optJSONObject(k).optLong("ts")) {
                    return false;
                }
            }
            return true;
        });
        Recorder.Event last = all.events().get(all.events().size() - 1);
        assertEquals(keys, last.keys);
        long prev = 0;
        for (String k : keys) {
            long ts = stored.getJSONObject(k).getLong("ts");
            // The emulator's clock and the server's are the same machine's, give or take.
            assertTrue("ts " + ts + " in [" + t0 + ", " + t1 + "]", ts >= t0 - 5_000 && ts <= t1 + 5_000);
            assertTrue("ts in order", ts >= prev);
            prev = ts;
        }
        assertEquals("device1", stored.getJSONObject(keys.get(0)).getString("text"));
        assertEquals("admin3", stored.getJSONObject(keys.get(5)).getString("text"));
    }

    // 4. Queries: orderByChild with .indexOn, limitToLast, a child entering the window.

    @Test
    public void queryWindowFollowsWrites() throws Exception {
        signInAnonymously();
        String board = "boards/android-" + run;
        int[] scores = {50, 10, 80, 30, 70, 20, 60, 40};
        StringBuilder seed = new StringBuilder("{");
        for (int i = 0; i < scores.length; i++) {
            seed.append(i == 0 ? "" : ",").append("\"p").append(i).append("\":{\"score\":").append(scores[i]).append('}');
        }
        assertEquals(200, rest("PUT", board, seed.append('}').toString()).status);

        Query top = db.getReference(board).orderByChild("score").limitToLast(3);
        Recorder value = listen(top);
        Recorder children = listenChildren(top);
        value.await("the top three", 10_000, e -> !e.isEmpty() && e.get(e.size() - 1).keys.equals(Arrays.asList("p6", "p4", "p2")));
        assertEquals(Arrays.asList("added p6 null", "added p4 p6", "added p2 p4"), children.describe());

        // This client adds a child above the window's lowest: it enters after p4, p6 leaves.
        await(db.getReference(board).child("p8").setValue(map("score", 75)));
        value.await("p8 in", 10_000, e -> e.get(e.size() - 1).keys.equals(Arrays.asList("p4", "p8", "p2")));
        assertTrue(children.describe().containsAll(Arrays.asList("added p8 p4", "removed p6 null")));

        // The admin writes below the window (nothing changes), then moves p1 to the top.
        int before = value.events().size();
        assertEquals(200, rest("PUT", board + "/p9", "{\"score\":5}").status);
        assertEquals(200, rest("PATCH", board, "{\"p1/score\":99}").status);
        value.await("p1 in", 10_000, e -> e.get(e.size() - 1).keys.equals(Arrays.asList("p8", "p2", "p1")));
        assertEquals(before + 1, value.events().size());
        assertTrue(children.describe().contains("removed p4 null"));

        // A one-off query the server answers from its index: the two lowest.
        DataSnapshot low = await(db.getReference(board).orderByChild("score").limitToFirst(2).get());
        List<String> lowKeys = new ArrayList<>();
        for (DataSnapshot c : low.getChildren()) {
            lowKeys.add(c.getKey());
        }
        assertEquals(Arrays.asList("p9", "p5"), lowKeys);
    }

    // 5. Transactions: concurrent increments from this client and the admin add up exactly.

    @Test
    public void concurrentTransactionsAddUpExactly() throws Exception {
        signInAnonymously();
        String path = "counters/android-" + run;
        assertEquals(200, rest("PUT", path, "0").status);
        int n = 20;
        ExecutorService pool = Executors.newFixedThreadPool(4);
        List<Future<Integer>> admin = new ArrayList<>();
        List<Task<Boolean>> device = new ArrayList<>();
        try {
            for (int i = 0; i < n; i++) {
                admin.add(pool.submit(() -> adminIncrement(path)));
                device.add(increment(db.getReference(path)));
            }
            int retries = 0;
            for (Future<Integer> f : admin) {
                retries += f.get(60, TimeUnit.SECONDS) - 1;
            }
            for (Task<Boolean> t : device) {
                assertTrue("committed", Tasks.await(t, 60, TimeUnit.SECONDS));
            }
            Log.i(TAG, "transactions: admin increments retried " + retries + " times");
        } finally {
            pool.shutdownNow();
        }
        assertEquals(String.valueOf(2 * n), rest("GET", path, null).body);
        assertEquals(2L * n, await(db.getReference(path).get()).getValue());
    }

    // 6. onDisconnect presence: the other party sees offline when a client dies or disconnects.

    @Test
    public void presenceGoesOfflineWhenTheClientDies() throws Exception {
        String watcher = signInAnonymously();
        Context ctx = InstrumentationRegistry.getInstrumentation().getTargetContext();
        TaskCompletionSource<Bundle> reply = new TaskCompletionSource<>();
        ResultReceiver receiver = new ResultReceiver(null) {
            @Override
            protected void onReceiveResult(int code, Bundle data) {
                if (code == 0) {
                    reply.trySetResult(data);
                } else {
                    reply.trySetException(new IllegalStateException("presence process: " + data.getString("error")));
                }
            }
        };
        ServiceConnection conn = new ServiceConnection() {
            @Override
            public void onServiceConnected(ComponentName name, IBinder service) {
            }

            @Override
            public void onServiceDisconnected(ComponentName name) {
            }
        };
        assertTrue(ctx.bindService(new Intent(ctx, PresenceService.class).putExtra("reply", plain(receiver)), conn, Context.BIND_AUTO_CREATE));
        Bundle b = await(reply.getTask());
        String other = b.getString("uid");
        int pid = b.getInt("pid");
        assertTrue("another process", pid != Process.myPid());

        Recorder status = listen(db.getReference("status").child(other));
        status.await("online", 10_000, e -> "online".equals(state(e)));
        // Unbound first, so the system does not start the service again; then the process dies.
        ctx.unbindService(conn);
        long t0 = System.currentTimeMillis();
        Process.killProcess(pid);
        status.await("offline after the kill", 15_000, e -> "offline".equals(state(e)));
        Log.i(TAG, "presence: offline " + (System.currentTimeMillis() - t0) + " ms after the kill");

        // This client disconnects cleanly: its own onDisconnect runs.
        DatabaseReference me = db.getReference("status").child(watcher);
        await(me.onDisconnect().setValue(map("state", "offline", "at", ServerValue.TIMESTAMP)));
        await(me.setValue(map("state", "online", "at", ServerValue.TIMESTAMP)));
        db.goOffline();
        try {
            eventually("offline after goOffline", 10_000, () -> rest("GET", "status/" + watcher + "/state", null).body.equals("\"offline\""));
        } finally {
            db.goOnline();
        }
        assertTrue(((JSONObject) rest("GET", "status/" + watcher, null).json()).get("at") instanceof Number);
    }

    // 7. Offline: with the server down, a write is applied locally and lands when it is back.

    @Test
    public void offlineWriteLandsWhenTheServerIsBack() throws Exception {
        String uid = signInAnonymously();
        Recorder connected = listen(db.getReference(".info/connected"));
        connected.await("connected", 30_000, e -> Boolean.TRUE.equals(last(e)));
        DatabaseReference notes = db.getReference("users").child(uid).child("notes");
        Recorder local = listenChildren(notes);
        await(notes.child("n0").setValue("online"));

        Task<Void> write;
        server("down");
        try {
            connected.await("disconnected", 30_000, e -> Boolean.FALSE.equals(last(e)));
            write = notes.child("n1").setValue(map("text", "written offline", "at", ServerValue.TIMESTAMP));
            local.await("the local event", 5_000, e -> e.size() == 2 && "n1".equals(e.get(1).key));
            Thread.sleep(3000);
            assertFalse("the write completed with the server down", write.isComplete());
        } finally {
            server("up");
        }
        // The SDK reconnects by itself (its backoff is at most 30 s) and sends the queued write.
        Tasks.await(write, 90, TimeUnit.SECONDS);
        JSONObject stored = (JSONObject) rest("GET", "users/" + uid + "/notes/n1", null).json();
        assertEquals("written offline", stored.getString("text"));
        assertTrue(stored.get("at") instanceof Number);
    }

    // 8. Revocation: an admin disables the user; the client's listens are cancelled within seconds.

    @Test
    public void disablingTheUserCancelsItsListens() throws Exception {
        String uid = await(auth.createUserWithEmailAndPassword("revoked-android-" + run + "@example.com", "secret1")).getUser().getUid();
        DatabaseReference mine = db.getReference("users").child(uid);
        await(mine.setValue(map("secret", 1)));
        Recorder own = listen(mine);
        Recorder room = listenChildren(db.getReference("rooms").child("android-revoked-" + run).child("messages"));
        own.await("the data", 10_000, e -> e.size() == 1);

        long t0 = System.currentTimeMillis();
        disableUser(uid);
        own.await("cancelled", 5_000, e -> "cancelled".equals(e.get(e.size() - 1).kind));
        room.await("cancelled", 5_000, e -> !e.isEmpty() && "cancelled".equals(e.get(e.size() - 1).kind));
        Log.i(TAG, "revocation: listens cancelled " + (System.currentTimeMillis() - t0) + " ms after the user was disabled");
        assertEquals(DatabaseError.PERMISSION_DENIED, own.events().get(own.events().size() - 1).error.getCode());
        assertEquals(DatabaseError.PERMISSION_DENIED, room.events().get(room.events().size() - 1).error.getCode());
        // The database SDK asked Auth for a fresh token, which a disabled user does not get: Auth
        // signs the user out.
        eventually("signed out", 15_000, () -> auth.getCurrentUser() == null);
    }

    // 9. The network guard.

    /** The in-process half of the guard sees the connections the platform opens. */
    @Test
    public void networkGuardSeesConnections() throws Exception {
        // 127.0.0.2 is loopback to the device guard and outside to NetworkGuard; nothing listens.
        HttpURLConnection c = (HttpURLConnection) new URL("https://127.0.0.2:9/probe").openConnection();
        c.setConnectTimeout(2000);
        try {
            c.getResponseCode();
            fail("connected to 127.0.0.2:9");
        } catch (java.io.IOException expected) {
            // refused
        } finally {
            c.disconnect();
        }
        List<String> outside = NetworkGuard.outside();
        assertEquals(Collections.singletonList("https://127.0.0.2"), outside.subList(outsideBefore, outside.size()));
        NetworkGuard.forget(outside.size() - outsideBefore);
        // Nothing this process opened went to a Firebase database host.
        for (String host : NetworkGuard.outside()) {
            assertFalse(host, host.endsWith(".firebaseio.com") || host.endsWith(".firebasedatabase.app"));
        }
    }

    // Helpers.

    private static <T> T await(Task<T> task) throws Exception {
        return Tasks.await(task, 30, TimeUnit.SECONDS);
    }

    private String signInAnonymously() throws Exception {
        return await(auth.signInAnonymously()).getUser().getUid();
    }

    private static Map<String, Object> map(Object... kv) {
        Map<String, Object> m = new HashMap<>();
        for (int i = 0; i < kv.length; i += 2) {
            m.put((String) kv[i], kv[i + 1]);
        }
        return m;
    }

    /** The DatabaseError code a write fails with, or null when it succeeds. */
    private static Integer writeError(DatabaseReference ref, Object value) throws Exception {
        TaskCompletionSource<Integer> done = new TaskCompletionSource<>();
        ref.setValue(value, (error, r) -> done.setResult(error == null ? -1 : error.getCode()));
        int code = await(done.getTask());
        return code == -1 ? null : code;
    }

    private static Task<Boolean> increment(DatabaseReference ref) {
        TaskCompletionSource<Boolean> done = new TaskCompletionSource<>();
        ref.runTransaction(new Transaction.Handler() {
            @Override
            public Transaction.Result doTransaction(MutableData current) {
                Object v = current.getValue();
                current.setValue((v == null ? 0 : ((Number) v).longValue()) + 1);
                return Transaction.success(current);
            }

            @Override
            public void onComplete(DatabaseError error, boolean committed, DataSnapshot snapshot) {
                if (error != null) {
                    done.setException(error.toException());
                } else {
                    done.setResult(committed);
                }
            }
        });
        return done.getTask();
    }

    /**
     * The receiver as a plain ResultReceiver: the presence process cannot load this test's anonymous
     * subclass, but a parcelled ResultReceiver is only a binder back to it.
     */
    private static ResultReceiver plain(ResultReceiver r) {
        Parcel p = Parcel.obtain();
        try {
            r.writeToParcel(p, 0);
            p.setDataPosition(0);
            return ResultReceiver.CREATOR.createFromParcel(p);
        } finally {
            p.recycle();
        }
    }

    private static Object last(List<Recorder.Event> e) {
        return e.isEmpty() ? null : e.get(e.size() - 1).value;
    }

    private static String state(List<Recorder.Event> e) {
        Object v = last(e);
        return v instanceof Map ? (String) ((Map<?, ?>) v).get("state") : null;
    }

    private static void eventually(String what, long ms, Callable<Boolean> cond) throws Exception {
        long end = System.currentTimeMillis() + ms;
        while (!cond.call()) {
            if (System.currentTimeMillis() > end) {
                throw new AssertionError("timed out waiting for " + what);
            }
            Thread.sleep(100);
        }
    }

    private Recorder listen(Query q) {
        Recorder r = new Recorder();
        q.addValueEventListener(r);
        cleanup.add(() -> q.removeEventListener((ValueEventListener) r));
        return r;
    }

    private Recorder listenChildren(Query q) {
        Recorder r = new Recorder();
        q.addChildEventListener(r);
        cleanup.add(() -> q.removeEventListener((ChildEventListener) r));
        return r;
    }

    /** Records the events of one listener: value, added, changed, removed, moved and cancelled. */
    static final class Recorder implements ValueEventListener, ChildEventListener {
        static final class Event {
            final String kind;
            final String key;
            final String prev;
            final Object value;
            final List<String> keys = new ArrayList<>();
            final DatabaseError error;

            Event(String kind, DataSnapshot s, String prev, DatabaseError error) {
                this.kind = kind;
                this.key = s == null ? null : s.getKey();
                this.prev = prev;
                this.value = s == null ? null : s.getValue();
                this.error = error;
                if (s != null) {
                    for (DataSnapshot c : s.getChildren()) {
                        keys.add(c.getKey());
                    }
                }
            }
        }

        private final List<Event> events = new ArrayList<>();

        synchronized List<Event> events() {
            return new ArrayList<>(events);
        }

        /** The child events as "kind key prev". */
        synchronized List<String> describe() {
            List<String> out = new ArrayList<>();
            for (Event e : events) {
                out.add(e.kind + " " + e.key + " " + e.prev);
            }
            return out;
        }

        synchronized void await(String what, long ms, Predicate<List<Event>> cond) throws InterruptedException {
            long end = System.currentTimeMillis() + ms;
            while (!cond.test(events)) {
                long left = end - System.currentTimeMillis();
                if (left <= 0) {
                    throw new AssertionError("timed out waiting for " + what + "; events: " + describe());
                }
                wait(left);
            }
        }

        private synchronized void add(Event e) {
            events.add(e);
            notifyAll();
        }

        @Override
        public void onDataChange(DataSnapshot s) {
            add(new Event("value", s, null, null));
        }

        @Override
        public void onChildAdded(DataSnapshot s, String prev) {
            add(new Event("added", s, prev, null));
        }

        @Override
        public void onChildChanged(DataSnapshot s, String prev) {
            add(new Event("changed", s, prev, null));
        }

        @Override
        public void onChildRemoved(DataSnapshot s) {
            add(new Event("removed", s, null, null));
        }

        @Override
        public void onChildMoved(DataSnapshot s, String prev) {
            add(new Event("moved", s, prev, null));
        }

        @Override
        public void onCancelled(DatabaseError error) {
            add(new Event("cancelled", null, null, error));
        }
    }

    // The second party: the database's REST API with an admin credential (the CLI token, which
    // bypasses the rules as a service account does), the admin Auth API and the server's switch.

    static final class Rest {
        int status;
        String body;
        String etag;

        Object json() throws Exception {
            return new JSONTokener(body).nextValue();
        }
    }

    private static Rest rest(String method, String path, String body, String... headers) throws Exception {
        HttpURLConnection c = (HttpURLConnection) new URL(arumaUrl + "/" + path + ".json?ns=" + ns).openConnection();
        // HttpURLConnection has no PATCH; the database takes the override, as firebase-admin-java sends it.
        c.setRequestMethod(method.equals("PATCH") ? "POST" : method);
        if (method.equals("PATCH")) {
            c.setRequestProperty("X-HTTP-Method-Override", "PATCH");
        }
        c.setRequestProperty("Authorization", "Bearer " + args.getString("cliToken", ""));
        for (int i = 0; i < headers.length; i += 2) {
            c.setRequestProperty(headers[i], headers[i + 1]);
        }
        return send(c, body);
    }

    private static Rest send(HttpURLConnection c, String body) throws Exception {
        try {
            if (body != null) {
                c.setDoOutput(true);
                c.setRequestProperty("Content-Type", "application/json");
                try (OutputStream out = c.getOutputStream()) {
                    out.write(body.getBytes(StandardCharsets.UTF_8));
                }
            }
            Rest r = new Rest();
            r.status = c.getResponseCode();
            InputStream in = r.status < 400 ? c.getInputStream() : c.getErrorStream();
            r.body = in == null ? "" : read(in);
            r.etag = c.getHeaderField("ETag");
            return r;
        } finally {
            c.disconnect();
        }
    }

    /**
     * Adds one to a number with a conditional REST write (ETag and if-match), as the Admin SDKs'
     * transactions over REST do, retrying when another writer came first. Returns the attempts.
     */
    private static int adminIncrement(String path) throws Exception {
        Rest cur = rest("GET", path, null, "X-Firebase-ETag", "true");
        for (int attempt = 1; attempt <= 100; attempt++) {
            long v = ((Number) cur.json()).longValue();
            Rest next = rest("PUT", path, String.valueOf(v + 1), "X-Firebase-ETag", "true", "if-match", cur.etag);
            if (next.status == 200) {
                return attempt;
            }
            assertEquals(next.body, 412, next.status);
            cur = next; // 412 carries the current value and its ETag
        }
        throw new AssertionError("increment of " + path + ": no luck in 100 attempts");
    }

    /** Disables a user as firebase-admin's updateUser(uid, disabled) does: accounts:update. */
    private static void disableUser(String uid) throws Exception {
        HttpURLConnection c = (HttpURLConnection) new URL(arumaUrl + "/identitytoolkit.googleapis.com/v1/projects/" + projectId + "/accounts:update").openConnection();
        c.setRequestMethod("POST");
        c.setRequestProperty("Authorization", "Bearer " + args.getString("cliToken", ""));
        Rest r = send(c, "{\"localId\":\"" + uid + "\",\"disableUser\":true}");
        assertEquals(r.body, 200, r.status);
    }

    /** Takes the whole server down (every connection closed, new ones refused) or up again. */
    private static void server(String state) throws Exception {
        HttpURLConnection c = (HttpURLConnection) new URL(args.getString("switchUrl", "") + "/" + state).openConnection();
        c.setRequestMethod("POST");
        Rest r = send(c, "");
        assertEquals(r.body, 200, r.status);
    }

    private static String read(InputStream in) throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buf = new byte[8192];
        for (int n; (n = in.read(buf)) > 0; ) {
            out.write(buf, 0, n);
        }
        return out.toString("UTF-8");
    }
}
