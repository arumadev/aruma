// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.net.URL;
import java.net.URLStreamHandlerFactory;
import java.util.concurrent.atomic.AtomicInteger;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

public class StorageGuardTest {
    private static final String GOOGLE = "https://firebasestorage.googleapis.com/v0";
    private final AtomicInteger installs = new AtomicInteger();

    @Before
    public void setUp() {
        ArumaRedirect.resetForTest(new ArumaRedirect.Installer() {
            @Override
            public void install(URLStreamHandlerFactory factory) {
                installs.incrementAndGet();
            }
        });
    }

    @After
    public void tearDown() {
        StorageGuard.resetForTest(StorageGuard.REFLECTION);
        ArumaRedirect.resetForTest(new ArumaRedirect.Installer() {
            @Override
            public void install(URLStreamHandlerFactory factory) {}
        });
    }

    private static void prodBaseUrl(final String value) {
        StorageGuard.resetForTest(new StorageGuard.Probe() {
            @Override
            public String prodBaseUrl() {
                return value;
            }
        });
    }

    @Test
    public void withoutFirebaseStorageThereIsNothingToCheck() {
        // The JVM tests have no firebase-storage: the real probe finds no NetworkRequest.
        assertNull(StorageGuard.check(false));
        prodBaseUrl(null);
        assertNull(StorageGuard.check(false));
        assertNull(StorageGuard.buildTimeBase());
    }

    @Test
    public void aBaseOnArumaMeansThePluginRan() {
        prodBaseUrl("https://aruma.example.com/v0");
        assertTrue(StorageGuard.check(false).contains("dev.aruma.storage"));
        assertEquals("https://aruma.example.com/v0", StorageGuard.buildTimeBase());
        assertTrue(StorageGuard.check(true).contains("https://aruma.example.com/v0"));
    }

    @Test
    public void googlesBaseWithTheRunTimeRedirectIsTheFallback() {
        prodBaseUrl(GOOGLE);
        assertTrue(StorageGuard.check(true).contains("fallback"));
        assertNull(StorageGuard.buildTimeBase());
    }

    @Test
    public void googlesBaseWithoutAnyRedirectStopsTheApp() {
        prodBaseUrl(GOOGLE);
        try {
            StorageGuard.check(false);
            fail();
        } catch (ArumaRedirectException e) {
            assertTrue(e.getMessage(), e.getMessage().contains("without the Aruma Gradle plugin dev.aruma.storage"));
            assertTrue(e.getMessage(), e.getMessage().contains("ArumaStorageRedirect is false"));
        }
        prodBaseUrl("https://FirebaseStorage.GoogleAPIs.com/v0");
        try {
            StorageGuard.check(false);
            fail();
        } catch (ArumaRedirectException expected) {
            // Host compared in lower case.
        }
    }

    @Test
    public void anUnreadableBaseStopsTheAppOnlyWithoutTheRedirect() {
        StorageGuard.resetForTest(new StorageGuard.Probe() {
            @Override
            public String prodBaseUrl() throws ReflectiveOperationException {
                throw new NoSuchFieldException("PROD_BASE_URL");
            }
        });
        assertTrue(StorageGuard.check(true).contains("cannot read"));
        try {
            StorageGuard.check(false);
            fail();
        } catch (ArumaRedirectException e) {
            assertTrue(e.getMessage(), e.getMessage().contains("cannot tell where Firebase Storage"));
        }
    }

    @Test
    public void theManifestSwitchReachesTheGuardAndNothingIsInstalledWhenItStops() throws Exception {
        prodBaseUrl(GOOGLE);
        try {
            ArumaRedirect.installFromValues("https://aruma.example.com", null, null, "false", false);
            fail();
        } catch (ArumaRedirectException expected) {
            assertTrue(expected.getMessage().contains("dev.aruma.storage"));
        }
        assertEquals(0, installs.get());
        assertNull(ArumaRedirect.baseUrl());

        // Absent means true: the run-time redirect is the fallback.
        assertTrue(ArumaRedirect.installFromValues("https://aruma.example.com", null, null, null, false));
        assertEquals(1, installs.get());
        assertEquals("https://aruma.example.com/firebasestorage.googleapis.com/v0/b/b/o/x",
                AuthEndpoints.rewrite(new URL("https://firebasestorage.googleapis.com/v0/b/b/o/x"),
                        new ArumaBase("https://aruma.example.com", false, false)));
    }

    @Test
    public void withThePluginTheRedirectCanBeSwitchedOff() throws Exception {
        prodBaseUrl("https://aruma.example.com/v0");
        assertTrue(ArumaRedirect.installFromValues("https://aruma.example.com", null, null, "false", false));
        assertNull(AuthEndpoints.rewrite(new URL("https://firebasestorage.googleapis.com/v0/b/b/o/x"),
                new ArumaBase("https://aruma.example.com", false, false)));
    }
}
