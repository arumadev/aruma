package com.aruma.e2e;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import android.os.Bundle;
import android.util.Log;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;

import dev.aruma.redirect.ArumaRedirect;

import org.junit.After;
import org.junit.AfterClass;
import org.junit.Before;
import org.junit.BeforeClass;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.net.HttpURLConnection;
import java.net.URL;
import java.util.Collections;
import java.util.List;

/**
 * The official firebase-storage for Android in production mode, pointed at Aruma at build time by
 * the Gradle plugin dev.aruma.storage (no useEmulator, no run-time redirect of Storage), signed in
 * with firebase-auth through aruma-redirect, against a real server under storage.rules, with every
 * other connection recorded here and rejected on the device (NetworkGuard, run.sh). The work is in
 * the app's own {@link Scenario}, so the release build runs it after R8.
 *
 * <p>run.sh passes arumaUrl (ArumaURL, Auth) and storageUrl (the origin the plugin was given in
 * upper case, here in the lower case the plugin must write).
 */
@RunWith(AndroidJUnit4.class)
public class StorageE2ETest {
    private static final String TAG = "ArumaE2E";
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
        assertEquals(storageUrl + "/v0", Scenario.prodBaseUrl());
        String notice = Scenario.notice();
        assertNotNull("META-INF/aruma/firebase-storage-MODIFIED.txt is not in the APK", notice);
        assertTrue(notice, notice.contains("modified copy of firebase-storage"));
        assertTrue(notice, notice.contains("\"" + storageUrl + "/v0\""));
    }

    @Test
    public void twelveOperations() throws Exception {
        Scenario.Result r = Scenario.twelveOperations(ApplicationProvider.getApplicationContext(), storageUrl);
        assertEquals("operations\n" + r, 12, r.lines.size());
        assertTrue("operations\n" + r, r.allPassed());
    }

    /** Names with a space, '+', non-ASCII and Arabic letters, in every form of their URL. */
    @Test
    public void encodedNamesInEveryForm() throws Exception {
        Scenario.Result r = Scenario.names(storageUrl);
        assertEquals("names\n" + r, 2, r.lines.size());
        assertTrue("names\n" + r, r.allPassed());
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
        // Nothing this process opened went to Google's Storage host.
        for (String host : NetworkGuard.outside()) {
            assertFalse(host, host.contains("googleapis.com"));
        }
    }
}
