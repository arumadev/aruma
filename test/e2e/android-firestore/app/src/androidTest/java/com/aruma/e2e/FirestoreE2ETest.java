package com.aruma.e2e;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;
import static org.junit.Assume.assumeTrue;

import android.content.Context;
import android.os.Bundle;
import android.util.Log;

import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;

import com.google.android.gms.tasks.Task;
import com.google.android.gms.tasks.Tasks;
import com.google.firebase.FirebaseApp;
import com.google.firebase.FirebaseOptions;
import com.google.firebase.Timestamp;
import com.google.firebase.auth.FirebaseAuth;
import com.google.firebase.firestore.CollectionReference;
import com.google.firebase.firestore.DocumentChange;
import com.google.firebase.firestore.DocumentReference;
import com.google.firebase.firestore.DocumentSnapshot;
import com.google.firebase.firestore.FieldPath;
import com.google.firebase.firestore.FieldValue;
import com.google.firebase.firestore.FirebaseFirestore;
import com.google.firebase.firestore.FirebaseFirestoreException;
import com.google.firebase.firestore.FirebaseFirestoreSettings;
import com.google.firebase.firestore.ListenerRegistration;
import com.google.firebase.firestore.MemoryCacheSettings;
import com.google.firebase.firestore.MetadataChanges;
import com.google.firebase.firestore.Query;
import com.google.firebase.firestore.QuerySnapshot;
import com.google.firebase.firestore.Source;
import com.google.firebase.firestore.WriteBatch;

import dev.aruma.redirect.ArumaRedirect;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.After;
import org.junit.AfterClass;
import org.junit.Before;
import org.junit.BeforeClass;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.Callable;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.function.Predicate;

/**
 * The official firebase-firestore for Android, with the host of the Aruma server set in code (the
 * SDK reads no other place; google-services.json carries the project), signed in with
 * firebase-auth through aruma-redirect, against a real server under the cloud.firestore rules of
 * firestore.rules, with every other connection recorded here and rejected on the device
 * (NetworkGuard, run.sh). The second party is a second FirebaseApp, and the REST API with an
 * admin credential. The cache is in memory: the resume token a listener keeps across
 * disableNetwork and enableNetwork is the SDK's, not a database's.
 *
 * <p>run.sh passes the server as instrumentation arguments: arumaUrl, projectId, cliToken, rules
 * and ruleset (the name of the released firestore.rules ruleset).
 */
@RunWith(AndroidJUnit4.class)
public class FirestoreE2ETest {
    private static final String TAG = "ArumaE2E";
    private static final Bundle args = InstrumentationRegistry.getArguments();
    private static final String arumaUrl = args.getString("arumaUrl", "");
    private static final String projectId = args.getString("projectId", "");
    private static final String host = "127.0.0.1:" + arumaUrl.substring(arumaUrl.lastIndexOf(':') + 1);
    /** The ruleset is released (firestore.rules, the default) and not dev_open_rules: run.sh's ARUMA_E2E_RULES. */
    private static final boolean released = "released".equals(args.getString("rules", "released"));
    /** Tells this run's documents apart from an earlier run's on the same server. */
    private static final String run = Long.toString(System.currentTimeMillis(), 36);

    private static FirebaseFirestore db;
    private static FirebaseFirestore otherDb;
    private static FirebaseAuth otherAuth;

    private FirebaseAuth auth;
    private int outsideBefore;
    private final List<Runnable> cleanup = new ArrayList<>();
    private final List<Recorder> recorders = new ArrayList<>();

    @BeforeClass
    public static void installGuard() {
        NetworkGuard.install();
        if (arumaUrl.isEmpty()) {
            return;
        }
        Context ctx = InstrumentationRegistry.getInstrumentation().getTargetContext();
        db = FirebaseFirestore.getInstance();
        db.setFirestoreSettings(settings());
        // A second client: the same project, an app and an anonymous user of its own.
        FirebaseApp app = FirebaseApp.initializeApp(ctx, new FirebaseOptions.Builder(FirebaseApp.getInstance().getOptions()).build(), "other");
        otherDb = FirebaseFirestore.getInstance(app);
        otherDb.setFirestoreSettings(settings());
        otherAuth = FirebaseAuth.getInstance(app);
    }

    private static FirebaseFirestoreSettings settings() {
        return new FirebaseFirestoreSettings.Builder()
                .setHost(host)
                .setSslEnabled(false)
                .setLocalCacheSettings(MemoryCacheSettings.newBuilder().build())
                .build();
    }

    @AfterClass
    public static void summary() {
        List<String> outside = NetworkGuard.outside();
        Log.i(TAG, "guard: " + outside.size() + " connections outside the device " + outside);
    }

    @Before
    public void setUp() throws Exception {
        assertFalse("run through test/e2e/android-firestore/run.sh (no arumaUrl argument)", arumaUrl.isEmpty());
        auth = FirebaseAuth.getInstance();
        auth.signOut();
        await(db.enableNetwork());
        outsideBefore = NetworkGuard.outside().size();
    }

    @After
    public void tearDown() throws Exception {
        for (Recorder r : recorders) {
            r.stop();
        }
        await(db.enableNetwork());
        await(otherDb.enableNetwork());
        for (Runnable r : cleanup) {
            r.run();
        }
        List<String> outside = NetworkGuard.outside();
        assertEquals("connections leaving the device", Collections.emptyList(), outside.subList(outsideBefore, outside.size()));
    }

    // 1. Production mode, sign-in, and the rules.

    @Test
    public void rulesAdmitTheSignedInAndRefuseTheRest() throws Exception {
        assumeTrue("needs ARUMA_E2E_RULES=released", released);
        assertEquals(arumaUrl, ArumaRedirect.baseUrl());
        assertEquals(projectId, db.getApp().getOptions().getProjectId());
        assertEquals(host, db.getFirestoreSettings().getHost());
        for (String name : new String[] {
                "com.google.firebase.analytics.FirebaseAnalytics",
                "com.google.firebase.installations.FirebaseInstallations"}) {
            try {
                Class.forName(name);
                fail(name + " is linked in");
            } catch (ClassNotFoundException expected) {
                // not in the app
            }
        }
        DocumentReference d = db.collection("e2e").document("rules-" + run);
        // Signed out: the rules say no, to a write, a get and a listener alike.
        assertEquals(FirebaseFirestoreException.Code.PERMISSION_DENIED, code(d.set(map("a", 1))));
        assertEquals(FirebaseFirestoreException.Code.PERMISSION_DENIED, code(d.get(Source.SERVER)));
        Recorder denied = new Recorder();
        denied.listen(d, MetadataChanges.EXCLUDE);
        denied.await("denied", 10_000, e -> e.size() == 1);
        assertEquals("error PERMISSION_DENIED", denied.last());
        // A list query and a query listener are judged too, not only single documents.
        Query all = db.collection("e2e");
        assertEquals(FirebaseFirestoreException.Code.PERMISSION_DENIED, code(all.get(Source.SERVER)));
        Recorder deniedQuery = new Recorder();
        deniedQuery.listenQuery(all);
        deniedQuery.await("denied query", 10_000, e -> e.size() == 1);
        assertEquals("error PERMISSION_DENIED", deniedQuery.last());

        String uid = signIn();
        assertEquals(28, uid.length());
        await(d.set(map("a", 1)));
        assertEquals(map("a", 1L), await(d.get(Source.SERVER)).getData());
        // Outside the rules' paths even a signed-in user is refused.
        assertEquals(FirebaseFirestoreException.Code.PERMISSION_DENIED,
                code(db.collection("private").document("x").set(map("a", 1))));
        // The admin (the CLI token) is not subject to the rules and sees what the SDK wrote.
        JSONObject admin = new JSONObject(rest("e2e/rules-" + run));
        assertEquals("1", admin.getJSONObject("fields").getJSONObject("a").getString("integerValue"));
        cleanup.add(() -> quietly(d.delete()));
    }

    // 1c. A release published while a listener is open takes the listener away.

    @Test
    public void denyAllReleaseRemovesAnOpenListener() throws Exception {
        assumeTrue("needs ARUMA_E2E_RULES=released", released);
        String original = args.getString("ruleset", "");
        assertFalse("run through run.sh (no ruleset argument)", original.isEmpty());
        signIn();
        otherSignIn();
        String col = "r-" + run;
        Query q = db.collection("e2e").document(col).collection("items");
        DocumentReference a = otherDb.collection("e2e").document(col).collection("items").document("a");
        Recorder r = new Recorder();
        r.listenQuery(q);
        r.await("empty", 10_000, e -> e.size() == 1);
        r.clear();
        await(a.set(map("n", 1)));
        r.await("a enters", 10_000, e -> e.contains("[ADDED a {n=1}]"));
        r.clear();
        try {
            String rules = "rules_version = '2';\nservice cloud.firestore { match /databases/{d}/documents { match /{p=**} { allow read, write: if false; } } }\n";
            String denyAll = new JSONObject(rulesApi("POST", "rulesets", new JSONObject().put("source", new JSONObject().put("files",
                    new JSONArray().put(new JSONObject().put("name", "firestore.rules").put("content", rules)))).toString())).getString("name");
            publish(denyAll);
            // The server re-judges the open listener against the new release and ends it.
            r.await("removed", 15_000, e -> e.contains("error PERMISSION_DENIED"));
            assertEquals("error PERMISSION_DENIED", r.last());
            assertEquals(FirebaseFirestoreException.Code.PERMISSION_DENIED, code(a.get(Source.SERVER)));
        } finally {
            publish(original);
        }
        // The first ruleset is back: the same client reads again.
        assertEquals(1L, await(a.get(Source.SERVER)).getLong("n").longValue());
        cleanup.add(() -> quietly(a.delete()));
    }

    // 2. A document listener sees this client's own write: serverTimestamp and increment.

    @Test
    public void documentListenerSeesTheWritesOfItsClient() throws Exception {
        signIn();
        DocumentReference d = db.collection("e2e").document("doc-" + run);
        Recorder r = new Recorder();
        r.listen(d, MetadataChanges.INCLUDE);
        // Nothing there: the server says so, and that is the first snapshot.
        r.await("missing", 10_000, e -> e.contains("missing"));
        r.clear();

        long t0 = System.currentTimeMillis();
        await(d.set(map("name", "سارة", "count", 1, "at", FieldValue.serverTimestamp())));
        // The local event has the time still unset; the acknowledged one has the server's.
        r.await("acknowledged", 10_000, e -> e.stream().anyMatch(x -> x.contains("at=@") && !x.contains("pending")));
        List<String> events = r.events();
        assertEquals("{at=null, count=1, name=سارة} pending", events.get(0));
        String acked = events.get(events.size() - 1);
        assertTrue(acked, acked.matches("\\{at=@\\d+, count=1, name=سارة\\}"));
        long at = Long.parseLong(acked.substring(acked.indexOf('@') + 1, acked.indexOf(',')));
        assertTrue("server time " + at + " near " + t0, Math.abs(at - t0) < 60_000);
        r.clear();

        await(d.update(map("count", FieldValue.increment(5))));
        r.await("six", 10_000, e -> !e.isEmpty() && e.get(e.size() - 1).contains("count=6") && !e.get(e.size() - 1).contains("pending"));
        for (String e : r.events()) {
            assertTrue(e, e.matches("\\{at=@\\d+, count=6, name=سارة\\}( pending)?"));
        }
        assertEquals(6L, await(d.get(Source.SERVER)).getLong("count").longValue());
        // The admin credential sees the increment's result, and the same time as the listener did.
        JSONObject fields = new JSONObject(rest("e2e/doc-" + run)).getJSONObject("fields");
        assertEquals("6", fields.getJSONObject("count").getString("integerValue"));
        r.clear();

        // 3. A delete is seen by the listener.
        await(d.delete());
        r.await("deleted", 10_000, e -> e.contains("missing"));
        assertEquals("missing", r.last());
        assertFalse(await(d.get(Source.SERVER)).exists());
    }

    // 1b. A query listener, and another client's writes entering, changing and leaving it.

    @Test
    public void queryListenerFollowsAnotherClientsWrites() throws Exception {
        signIn();
        otherSignIn();
        String col = "q-" + run;
        Query q = db.collection("e2e").document(col).collection("items").whereGreaterThan("n", 1).orderBy("n");
        Recorder r = new Recorder();
        r.listenQuery(q);
        r.await("empty", 10_000, e -> e.size() == 1);
        assertEquals("[]", r.last());
        r.clear();

        DocumentReference a = otherDb.collection("e2e").document(col).collection("items").document("a");
        DocumentReference b = otherDb.collection("e2e").document(col).collection("items").document("b");
        await(a.set(map("n", 5)));
        r.await("a enters", 10_000, e -> e.size() == 1);
        assertEquals("[ADDED a {n=5}]", r.last());
        await(b.set(map("n", 1)));    // below the filter: not seen
        await(b.update(map("n", 3))); // enters
        r.await("b enters", 10_000, e -> e.size() == 2);
        assertEquals("[ADDED b {n=3}]", r.last());
        await(a.update(map("n", 2))); // changes, and moves before b
        r.await("a changes", 10_000, e -> e.size() == 3);
        assertEquals("[MODIFIED a {n=2}]", r.last());
        await(b.update(map("n", 0))); // leaves
        r.await("b leaves", 10_000, e -> e.size() == 4);
        assertEquals("[REMOVED b {n=3}]", r.last());
        await(a.delete());
        r.await("a deleted", 10_000, e -> e.size() == 5);
        assertEquals("[REMOVED a {n=2}]", r.last());
        assertEquals(Collections.emptyList(), docIds(await(q.get(Source.SERVER))));
        cleanup.add(() -> quietly(b.delete()));
    }

    // 4. get() from the server, of a document and of a query.

    @Test
    public void getFromTheServer() throws Exception {
        signIn();
        otherSignIn();
        String col = "g-" + run;
        DocumentReference theirs = otherDb.collection("e2e").document(col).collection("items").document("x");
        await(theirs.set(map("n", 7, "tags", Arrays.asList("a", "b"), "nested", map("k", true))));
        DocumentReference mine = db.collection("e2e").document(col).collection("items").document("x");
        DocumentSnapshot s = await(mine.get(Source.SERVER));
        assertTrue(s.exists());
        assertFalse(s.getMetadata().isFromCache());
        assertEquals(new TreeMap<>(map("n", 7L, "tags", Arrays.asList("a", "b"), "nested", map("k", true))), new TreeMap<>(s.getData()));
        QuerySnapshot qs = await(db.collection("e2e").document(col).collection("items").orderBy("n").get(Source.SERVER));
        assertEquals(Arrays.asList("x"), docIds(qs));
        assertFalse(qs.getMetadata().isFromCache());
        assertFalse(await(db.collection("e2e").document(col + "-none").get(Source.SERVER)).exists());
        cleanup.add(() -> quietly(theirs.delete()));
    }

    // 5. A WriteBatch lands in one snapshot.

    @Test
    public void writeBatchLandsAtOnce() throws Exception {
        signIn();
        String col = "b-" + run;
        CollectionReference items = db.collection("e2e").document(col).collection("items");
        await(items.document("upd").set(map("v", 1)));
        await(items.document("del").set(map("v", 1)));
        Recorder r = new Recorder();
        r.listenQuery(items.orderBy(FieldPath.documentId()));
        r.await("initial", 10_000, e -> e.size() == 1);
        assertEquals("[ADDED del {v=1}, ADDED upd {v=1}]", r.last());
        r.clear();

        WriteBatch batch = db.batch();
        batch.set(items.document("new"), map("v", 1, "at", FieldValue.serverTimestamp()));
        batch.update(items.document("upd"), map("v", FieldValue.increment(10)));
        batch.delete(items.document("del"));
        await(batch.commit());
        r.await("batch", 10_000, e -> e.stream().anyMatch(x -> x.contains("at=@")));
        // The three writes are one change set, however many snapshots the SDK raised for the
        // pending state and the acknowledgement.
        List<String> events = r.events();
        assertEquals("[REMOVED del {v=1}, ADDED new {at=null, v=1}, MODIFIED upd {v=11}]", events.get(0));
        assertTrue(events.toString(), events.get(events.size() - 1).matches("\\[MODIFIED new \\{at=@\\d+, v=1\\}\\]"));
        assertEquals(Arrays.asList("new", "upd"), docIds(await(items.orderBy(FieldPath.documentId()).get(Source.SERVER))));
        assertEquals(11L, await(items.document("upd").get(Source.SERVER)).getLong("v").longValue());
        assertFalse(await(items.document("del").get(Source.SERVER)).exists());
        // Under released rules, a batch that fails a rule changes nothing: its second write is
        // outside the rules.
        WriteBatch bad = db.batch();
        bad.set(items.document("never"), map("v", 1));
        bad.set(db.collection("private").document("y"), map("v", 1));
        if (released) {
            assertEquals(FirebaseFirestoreException.Code.PERMISSION_DENIED, code(bad.commit()));
            assertFalse(await(items.document("never").get(Source.SERVER)).exists());
        }
        cleanup.add(() -> {
            quietly(items.document("new").delete());
            quietly(items.document("upd").delete());
        });
    }

    // 6. Offline and online again: the listener resumes with its token and catches up.

    @Test
    public void listenerResumesAfterOfflineAndOnline() throws Exception {
        signIn();
        otherSignIn();
        String col = "o-" + run;
        CollectionReference mine = db.collection("e2e").document(col).collection("items");
        CollectionReference theirs = otherDb.collection("e2e").document(col).collection("items");
        await(mine.document("keep").set(map("v", 1)));
        await(mine.document("gone").set(map("v", 1)));
        await(mine.document("edit").set(map("v", 1)));
        Recorder r = new Recorder();
        r.listenQuery(mine.orderBy(FieldPath.documentId()));
        r.await("initial", 10_000, e -> e.size() == 1);
        assertEquals("[ADDED edit {v=1}, ADDED gone {v=1}, ADDED keep {v=1}]", r.last());
        r.clear();

        await(db.disableNetwork());
        // While this client is away the other one edits, deletes and adds.
        await(theirs.document("edit").set(map("v", 2)));
        await(theirs.document("gone").delete());
        await(theirs.document("added").set(map("v", 1)));
        Thread.sleep(1500);
        assertEquals("nothing arrives while offline", Collections.emptyList(), r.events());
        // A write made offline is applied locally at once, and only waits for the network.
        Recorder offline = new Recorder();
        offline.listen(mine.document("offline"), MetadataChanges.EXCLUDE);
        offline.await("missing", 10_000, e -> e.contains("missing cache"));
        Task<Void> pending = mine.document("offline").set(map("v", 9));
        offline.await("local write", 10_000, e -> e.contains("{v=9} pending"));
        Thread.sleep(500);
        assertFalse("the write waits for the network", pending.isComplete());
        r.clear();

        await(db.enableNetwork());
        await(pending);
        eventually("caught up", 15_000, () -> r.merged().equals(
                "added {v=1}, edit {v=2}, keep {v=1}, offline {v=9}"));
        // What arrived was one change set from the other client's work plus this one's own write.
        assertTrue(r.events().toString(), r.events().stream().anyMatch(x -> x.contains("REMOVED gone {v=1}")));
        assertEquals(Arrays.asList("added", "edit", "keep", "offline"), docIds(await(mine.orderBy(FieldPath.documentId()).get(Source.SERVER))));
        // And the listener is live again.
        r.clear();
        await(theirs.document("keep").set(map("v", 3)));
        r.await("live", 10_000, e -> e.contains("[MODIFIED keep {v=3}]"));
        cleanup.add(() -> {
            for (String id : new String[] {"added", "edit", "keep", "offline", "gone"}) {
                quietly(mine.document(id).delete());
            }
        });
    }

    // helpers

    private static <T> T await(Task<T> task) throws Exception {
        return Tasks.await(task, 30, TimeUnit.SECONDS);
    }

    private String signIn() throws Exception {
        return await(auth.signInAnonymously()).getUser().getUid();
    }

    private String otherSignIn() throws Exception {
        return await(otherAuth.signInAnonymously()).getUser().getUid();
    }

    private static Map<String, Object> map(Object... kv) {
        Map<String, Object> m = new HashMap<>();
        for (int i = 0; i < kv.length; i += 2) {
            m.put((String) kv[i], kv[i + 1]);
        }
        return m;
    }

    private static FirebaseFirestoreException.Code code(Task<?> task) throws Exception {
        try {
            await(task);
        } catch (ExecutionException e) {
            assertTrue(String.valueOf(e.getCause()), e.getCause() instanceof FirebaseFirestoreException);
            return ((FirebaseFirestoreException) e.getCause()).getCode();
        }
        throw new AssertionError("the task succeeded");
    }

    private static void quietly(Task<?> task) {
        try {
            await(task);
        } catch (Exception ignored) {
            // cleanup only
        }
    }

    private static List<String> docIds(QuerySnapshot s) {
        List<String> ids = new ArrayList<>();
        for (DocumentSnapshot d : s.getDocuments()) {
            ids.add(d.getId());
        }
        return ids;
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

    /** A GET on the REST API of the Document Database with the admin credential (the CLI token). */
    /** Points the cloud.firestore release at a ruleset, as a deploy does. */
    private static void publish(String ruleset) throws Exception {
        rulesApi("PATCH", "releases/cloud.firestore", new JSONObject().put("release", new JSONObject()
                .put("name", "projects/" + projectId + "/releases/cloud.firestore").put("rulesetName", ruleset)).toString());
    }

    /** The firebaserules API with the admin credential: method on projects/{project}/{path}. */
    private static String rulesApi(String method, String path, String body) throws Exception {
        HttpURLConnection c = (HttpURLConnection) new URL(arumaUrl + "/v1/projects/" + projectId + "/" + path).openConnection();
        c.setRequestMethod(method);
        c.setRequestProperty("Authorization", "Bearer " + args.getString("cliToken", ""));
        c.setRequestProperty("Content-Type", "application/json");
        c.setDoOutput(true);
        try {
            c.getOutputStream().write(body.getBytes(StandardCharsets.UTF_8));
            int status = c.getResponseCode();
            InputStream in = status < 400 ? c.getInputStream() : c.getErrorStream();
            String text = in == null ? "" : new String(in.readAllBytes(), StandardCharsets.UTF_8);
            assertEquals(text, 200, status);
            return text;
        } finally {
            c.disconnect();
        }
    }

    private static String rest(String path) throws Exception {
        HttpURLConnection c = (HttpURLConnection) new URL(arumaUrl + "/v1/projects/" + projectId + "/databases/(default)/documents/" + path).openConnection();
        c.setRequestProperty("Authorization", "Bearer " + args.getString("cliToken", ""));
        try {
            int status = c.getResponseCode();
            InputStream in = status < 400 ? c.getInputStream() : c.getErrorStream();
            String text = in == null ? "" : new String(in.readAllBytes(), StandardCharsets.UTF_8);
            assertEquals(text, 200, status);
            return text;
        } finally {
            c.disconnect();
        }
    }

    /** Records what a listener delivers, one readable line per snapshot. */
    final class Recorder {
        private final List<String> events = Collections.synchronizedList(new ArrayList<>());
        private final Map<String, String> view = Collections.synchronizedMap(new TreeMap<>());
        private ListenerRegistration reg;

        Recorder() {
            recorders.add(this);
        }

        void listen(DocumentReference d, MetadataChanges m) {
            reg = d.addSnapshotListener(m, (s, e) -> {
                if (e != null) {
                    events.add("error " + e.getCode());
                    return;
                }
                String text = s.exists() ? render(s.getData()) : "missing";
                if (s.exists() && s.getMetadata().hasPendingWrites()) {
                    text += " pending";
                }
                if (s.getMetadata().isFromCache() && !s.getMetadata().hasPendingWrites()) {
                    text += " cache";
                }
                events.add(text);
            });
        }

        void listenQuery(Query q) {
            reg = q.addSnapshotListener((s, e) -> {
                if (e != null) {
                    events.add("error " + e.getCode());
                    return;
                }
                List<String> changes = new ArrayList<>();
                for (DocumentChange c : s.getDocumentChanges()) {
                    changes.add(c.getType() + " " + c.getDocument().getId() + " " + render(c.getDocument().getData()));
                    if (c.getType() == DocumentChange.Type.REMOVED) {
                        view.remove(c.getDocument().getId());
                    } else {
                        view.put(c.getDocument().getId(), render(c.getDocument().getData()));
                    }
                }
                events.add(changes.toString());
            });
        }

        /** The documents the snapshots left standing, as "id data, ...". */
        String merged() {
            List<String> out = new ArrayList<>();
            synchronized (view) {
                for (Map.Entry<String, String> e : view.entrySet()) {
                    out.add(e.getKey() + " " + e.getValue());
                }
            }
            return String.join(", ", out);
        }

        List<String> events() {
            synchronized (events) {
                return new ArrayList<>(events);
            }
        }

        String last() {
            List<String> e = events();
            return e.isEmpty() ? null : e.get(e.size() - 1);
        }

        void clear() {
            events.clear();
        }

        void await(String what, long ms, Predicate<List<String>> cond) throws Exception {
            long end = System.currentTimeMillis() + ms;
            while (!cond.test(events())) {
                if (System.currentTimeMillis() > end) {
                    throw new AssertionError("timed out waiting for " + what + "; saw " + events());
                }
                Thread.sleep(50);
            }
        }

        void stop() {
            if (reg != null) {
                reg.remove();
            }
        }
    }

    /** A value as one line: keys sorted, a Timestamp as @millis. */
    private static String render(Map<String, Object> data) {
        List<String> parts = new ArrayList<>();
        for (Map.Entry<String, Object> e : new TreeMap<>(data).entrySet()) {
            Object v = e.getValue();
            parts.add(e.getKey() + "=" + (v instanceof Timestamp ? "@" + ((Timestamp) v).toDate().getTime() : String.valueOf(v)));
        }
        return "{" + String.join(", ", parts) + "}";
    }
}
