package com.aruma.e2e;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import android.os.Bundle;
import android.util.Log;

import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;

import com.google.android.gms.tasks.Task;
import com.google.android.gms.tasks.Tasks;
import com.google.firebase.FirebaseApp;
import com.google.firebase.auth.AuthResult;
import com.google.firebase.auth.EmailAuthProvider;
import com.google.firebase.auth.FirebaseAuth;
import com.google.firebase.auth.FirebaseAuthException;
import com.google.firebase.auth.FirebaseUser;
import com.google.firebase.auth.GetTokenResult;
import com.google.firebase.auth.UserProfileChangeRequest;

import dev.aruma.redirect.ArumaRedirect;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.After;
import org.junit.AfterClass;
import org.junit.Before;
import org.junit.BeforeClass;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.BufferedReader;
import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.concurrent.Callable;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;

/**
 * The official firebase-auth for Android in production mode (no useEmulator), configured only with
 * the google-services.json Aruma generated and sent to Aruma by aruma-redirect (ArumaURL in the
 * manifest), against a real server, with every other connection recorded here and rejected on the
 * device (NetworkGuard, run.sh). The scenarios follow test/e2e/web-auth and
 * docs/reference/compatibility.md.
 *
 * <p>run.sh passes the server as instrumentation arguments: arumaUrl, projectId and cliToken.
 */
@RunWith(AndroidJUnit4.class)
public class AuthE2ETest {
    private static final String TAG = "ArumaE2E";
    private static final Bundle args = InstrumentationRegistry.getArguments();
    private static final String arumaUrl = args.getString("arumaUrl", "");
    private static final String projectId = args.getString("projectId", "");
    /** Tells this run's addresses apart from an earlier run's on the same server. */
    private static final String run = Long.toString(System.currentTimeMillis(), 36);

    private FirebaseAuth auth;
    private int outsideBefore;

    @BeforeClass
    public static void installGuard() {
        NetworkGuard.install();
    }

    @AfterClass
    public static void summary() {
        List<String> outside = NetworkGuard.outside();
        Log.i(TAG, "guard: " + redirected().size() + " Auth requests to Google hosts redirected by aruma-redirect, "
                + outside.size() + " connections outside the device " + outside);
    }

    @Before
    public void setUp() {
        assertFalse("run through test/e2e/android-auth/run.sh (no arumaUrl argument)", arumaUrl.isEmpty());
        auth = FirebaseAuth.getInstance();
        auth.signOut();
        outsideBefore = NetworkGuard.outside().size();
    }

    @After
    public void noConnectionLeftTheDevice() {
        List<String> outside = NetworkGuard.outside();
        assertEquals("connections leaving the device", Collections.emptyList(), outside.subList(outsideBefore, outside.size()));
    }

    private static String email(String name) {
        return name + "-android-" + run + "@example.com";
    }

    private static <T> T await(Task<T> task) throws Exception {
        return Tasks.await(task, 30, TimeUnit.SECONDS);
    }

    /** The FirebaseAuthException error code a call fails with, or null when it succeeds. */
    private static String errorCode(Callable<Task<?>> call) throws Exception {
        try {
            await(call.call());
            return null;
        } catch (ExecutionException e) {
            if (e.getCause() instanceof FirebaseAuthException) {
                return ((FirebaseAuthException) e.getCause()).getErrorCode();
            }
            throw e;
        }
    }

    /**
     * Host and path of each request aruma-redirect rewrote in this process, from its log
     * (ArumaRedirectLog in the manifest). An app may read its own log lines.
     */
    private static List<String> redirected() {
        List<String> out = new ArrayList<>();
        try {
            Process p = Runtime.getRuntime().exec(new String[] {"logcat", "-d", "-v", "raw", "-s", "ArumaRedirect:I"});
            try (BufferedReader r = new BufferedReader(new InputStreamReader(p.getInputStream(), StandardCharsets.UTF_8))) {
                for (String line; (line = r.readLine()) != null; ) {
                    if (line.startsWith("redirected ")) {
                        out.add(line.substring("redirected ".length()));
                    }
                }
            }
            p.waitFor();
        } catch (Exception e) {
            throw new AssertionError("cannot read the ArumaRedirect log", e);
        }
        return out;
    }

    @Test
    public void productionModeThroughArumaRedirect() {
        // The library's provider read ArumaURL from the manifest before Firebase started.
        assertEquals(arumaUrl, ArumaRedirect.baseUrl());
        assertEquals(projectId, FirebaseApp.getInstance().getOptions().getProjectId());
        // Only firebase-auth and its dependencies are linked: Installations and Analytics would call
        // Google whatever aruma-redirect does.
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
    }

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
    }

    @Test
    public void emailPasswordLifecycle() throws Exception {
        String addr = email("life");
        AuthResult created = await(auth.createUserWithEmailAndPassword(addr, "secret1"));
        String uid = created.getUser().getUid();
        assertEquals(28, uid.length());
        assertEquals(addr, created.getUser().getEmail());

        auth.signOut();
        assertNull(auth.getCurrentUser());

        FirebaseUser user = await(auth.signInWithEmailAndPassword(addr, "secret1")).getUser();
        assertEquals(uid, user.getUid());

        // A forced refresh goes to the Secure Token endpoint and yields a token Aruma issued.
        int before = redirected().size();
        GetTokenResult token = await(user.getIdToken(true));
        List<String> after = redirected();
        assertTrue(after.toString(), after.subList(before, after.size()).contains("securetoken.googleapis.com/v1/token"));
        assertEquals(arumaUrl + "/issuers/" + projectId, token.getClaims().get("iss"));
        assertEquals(projectId, token.getClaims().get("aud"));
        assertEquals(uid, token.getClaims().get("sub"));
        assertEquals("password", token.getSignInProvider());

        await(user.updateProfile(new UserProfileChangeRequest.Builder().setDisplayName("سارة").build()));
        await(user.reload());
        assertEquals("سارة", auth.getCurrentUser().getDisplayName());

        // A password change after a recent sign-in keeps this session.
        await(user.reauthenticate(EmailAuthProvider.getCredential(addr, "secret1")));
        await(user.updatePassword("secret2"));
        await(user.getIdToken(true));
        auth.signOut();
        assertEquals("ERROR_INVALID_CREDENTIAL", errorCode(() -> auth.signInWithEmailAndPassword(addr, "secret1")));
        FirebaseUser again = await(auth.signInWithEmailAndPassword(addr, "secret2")).getUser();
        assertEquals(uid, again.getUid());

        await(again.delete());
        assertNull(auth.getCurrentUser());
        assertEquals("ERROR_INVALID_CREDENTIAL", errorCode(() -> auth.signInWithEmailAndPassword(addr, "secret2")));
    }

    @Test
    public void errorCodes() throws Exception {
        String addr = email("errors");
        await(auth.createUserWithEmailAndPassword(addr, "secret1"));
        auth.signOut();

        assertEquals("ERROR_EMAIL_ALREADY_IN_USE", errorCode(() -> auth.createUserWithEmailAndPassword(addr, "secret1")));
        assertEquals("ERROR_WEAK_PASSWORD", errorCode(() -> auth.createUserWithEmailAndPassword("weak-" + addr, "123")));
        assertEquals("ERROR_INVALID_EMAIL", errorCode(() -> auth.createUserWithEmailAndPassword("not-an-email", "secret1")));
        assertEquals("ERROR_INVALID_CREDENTIAL", errorCode(() -> auth.signInWithEmailAndPassword(addr, "wrong!!")));
        assertEquals("ERROR_INVALID_CREDENTIAL", errorCode(() -> auth.signInWithEmailAndPassword(email("nobody"), "secret1")));
        assertNull(auth.getCurrentUser());

        await(await(auth.signInWithEmailAndPassword(addr, "secret1")).getUser().delete());
    }

    @Test
    public void anonymous() throws Exception {
        FirebaseUser user = await(auth.signInAnonymously()).getUser();
        assertTrue(user.isAnonymous());
        assertEquals(28, user.getUid().length());
        GetTokenResult token = await(user.getIdToken(true));
        assertEquals("anonymous", token.getSignInProvider());
        assertEquals(user.getUid(), token.getClaims().get("sub"));
        await(user.delete());
        assertNull(auth.getCurrentUser());
    }

    @Test
    public void passwordResetThroughDevMailbox() throws Exception {
        String addr = email("reset");
        await(auth.createUserWithEmailAndPassword(addr, "secret1"));
        auth.signOut();
        await(auth.sendPasswordResetEmail(addr));
        String code = mailboxCode(addr);

        assertEquals(addr, await(auth.verifyPasswordResetCode(code)));
        assertEquals("ERROR_WEAK_PASSWORD", errorCode(() -> auth.confirmPasswordReset(code, "123")));
        await(auth.confirmPasswordReset(code, "secret2"));
        assertEquals("ERROR_INVALID_ACTION_CODE", errorCode(() -> auth.confirmPasswordReset(code, "secret3")));

        assertEquals("ERROR_INVALID_CREDENTIAL", errorCode(() -> auth.signInWithEmailAndPassword(addr, "secret1")));
        FirebaseUser user = await(auth.signInWithEmailAndPassword(addr, "secret2")).getUser();
        assertEquals(addr, user.getEmail());
        await(user.delete());
    }

    /**
     * The oobCode of the newest dev mailbox message to an address, as the user would follow the link
     * (the console API, with the CLI token run.sh passes).
     */
    private static String mailboxCode(String addr) throws Exception {
        HttpURLConnection c = (HttpURLConnection) new URL(arumaUrl + "/console/v1/projects/" + projectId + "/auth/mailbox").openConnection();
        c.setRequestProperty("Authorization", "Bearer " + args.getString("cliToken", ""));
        try {
            assertEquals(200, c.getResponseCode());
            JSONArray messages = new JSONObject(read(c.getInputStream())).getJSONArray("messages");
            for (int i = 0; i < messages.length(); i++) {
                JSONObject m = messages.getJSONObject(i);
                if (addr.equals(m.getString("to"))) {
                    String code = android.net.Uri.parse(m.getString("link")).getQueryParameter("oobCode");
                    assertNotNull(code);
                    return code;
                }
            }
            throw new AssertionError("no message to " + addr + " in the dev mailbox");
        } finally {
            c.disconnect();
        }
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
