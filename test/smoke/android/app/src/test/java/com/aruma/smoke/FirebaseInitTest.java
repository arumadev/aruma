package com.aruma.smoke;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertTrue;

import android.content.Context;
import androidx.test.core.app.ApplicationProvider;
import com.google.firebase.FirebaseApp;
import com.google.firebase.FirebaseOptions;
import com.google.firebase.installations.FirebaseInstallations;
import java.io.IOException;
import java.lang.reflect.Method;
import java.net.Socket;
import java.net.SocketImpl;
import java.net.URL;
import java.net.URLStreamHandler;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import org.json.JSONObject;
import org.junit.AfterClass;
import org.junit.BeforeClass;
import org.junit.Test;
import org.junit.runner.RunWith;
import org.robolectric.RobolectricTestRunner;
import org.robolectric.annotation.Config;

/**
 * Initialises Firebase from the resources the official google-services plugin generated out of
 * Aruma's google-services.json, with all network access blocked.
 */
@RunWith(RobolectricTestRunner.class)
@Config(sdk = 35)
public class FirebaseInitTest {
    private static final List<String> attempts = Collections.synchronizedList(new ArrayList<>());

    /** Records and refuses any HTTP(S) connection or raw socket. */
    @BeforeClass
    @SuppressWarnings("deprecation")
    public static void blockNetwork() throws IOException {
        URL.setURLStreamHandlerFactory(protocol -> {
            if (!protocol.equals("http") && !protocol.equals("https")) {
                return null;
            }
            return new URLStreamHandler() {
                @Override
                protected java.net.URLConnection openConnection(URL u) throws IOException {
                    attempts.add(u.toString());
                    throw new IOException("network access blocked by the smoke test: " + u);
                }
            };
        });
        Socket.setSocketImplFactory(() -> {
            attempts.add("socket");
            throw new IllegalStateException("network access blocked by the smoke test");
        });
    }

    @AfterClass
    public static void noNetwork() {
        assertTrue("network access attempted: " + attempts, attempts.isEmpty());
    }

    private static JSONObject expected() throws Exception {
        return new JSONObject(new String(Files.readAllBytes(Path.of(System.getProperty("aruma.expected")))));
    }

    @Test
    public void initializesFromGeneratedResources() throws Exception {
        Context context = ApplicationProvider.getApplicationContext();
        FirebaseApp app = FirebaseApp.initializeApp(context);
        assertNotNull("google-services resources missing or rejected", app);

        JSONObject exp = expected();
        JSONObject android = exp.getJSONObject("android");
        FirebaseOptions o = app.getOptions();
        assertEquals(android.getString("appId"), o.getApplicationId());
        assertEquals(android.getString("apiKey"), o.getApiKey());
        assertEquals(exp.getString("projectId"), o.getProjectId());
        assertEquals(exp.getString("projectNumber"), o.getGcmSenderId());
        assertEquals(exp.getString("storageBucket"), o.getStorageBucket());
        assertEquals(exp.getString("databaseURL"), o.getDatabaseUrl());
    }

    /**
     * Runs the Installations SDK's own pre-condition checks, which every product that needs an
     * installation (Messaging, Remote Config, Analytics...) applies: the app id format and the
     * API key pattern ("A" + 38 of [A-Za-z0-9_-]). They throw on an unacceptable config.
     */
    @Test
    public void installationsAcceptsAppIdAndApiKey() throws Exception {
        Context context = ApplicationProvider.getApplicationContext();
        FirebaseApp app = FirebaseApp.getApps(context).isEmpty() ? FirebaseApp.initializeApp(context) : FirebaseApp.getInstance();
        FirebaseInstallations fis = FirebaseInstallations.getInstance(app);
        Method checks = FirebaseInstallations.class.getDeclaredMethod("preConditionChecks");
        checks.setAccessible(true);
        checks.invoke(fis);
    }
}
