// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.net.URLStreamHandlerFactory;
import java.util.concurrent.atomic.AtomicInteger;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

public class ArumaRedirectTest {
    private final AtomicInteger installs = new AtomicInteger();
    private URLStreamHandlerFactory last;

    @Before
    public void setUp() {
        installs.set(0);
        ArumaRedirect.resetForTest(new ArumaRedirect.Installer() {
            @Override
            public void install(URLStreamHandlerFactory factory) {
                installs.incrementAndGet();
                last = factory;
            }
        });
    }

    @After
    public void tearDown() {
        ArumaRedirect.resetForTest(new ArumaRedirect.Installer() {
            @Override
            public void install(URLStreamHandlerFactory factory) {}
        });
    }

    @Test
    public void withoutArumaUrlNothingHappens() {
        assertFalse(ArumaRedirect.installFromValues(null, null, null, false));
        assertFalse(ArumaRedirect.installFromValues("", null, null, false));
        assertFalse(ArumaRedirect.installFromValues("   ", Boolean.TRUE, null, false));
        assertEquals(0, installs.get());
        assertNull(ArumaRedirect.baseUrl());
    }

    @Test
    public void validMetaDataInstallsOnce() {
        assertTrue(ArumaRedirect.installFromValues("https://aruma.example.com/", null, null, false));
        assertEquals(1, installs.get());
        assertEquals("https://aruma.example.com", ArumaRedirect.baseUrl());
        assertTrue(last.createURLStreamHandler("https") != null);
        assertNull(last.createURLStreamHandler("http"));
        assertNull(last.createURLStreamHandler("file"));
    }

    @Test
    public void invalidMetaDataThrowsAndInstallsNothing() {
        try {
            ArumaRedirect.installFromValues("not a url", null, null, false);
            fail();
        } catch (ArumaRedirectException expected) {
            assertTrue(expected.getMessage().contains("not a url"));
        }
        try {
            ArumaRedirect.installFromValues("http://aruma.example.com", null, null, false);
            fail();
        } catch (ArumaRedirectException expected) {
            assertTrue(expected.getMessage().contains("ArumaAllowInsecureURL"));
        }
        assertEquals(0, installs.get());
        assertNull(ArumaRedirect.baseUrl());
    }

    @Test
    public void emulatorHostFromTheManifestNeedsADebuggableApp() {
        try {
            ArumaRedirect.installFromValues("http://10.0.2.2:8095", null, null, false);
            fail();
        } catch (ArumaRedirectException expected) {
            assertTrue(expected.getMessage().contains("ArumaAllowInsecureURL"));
        }
        assertNull(ArumaRedirect.baseUrl());
        assertTrue(ArumaRedirect.installFromValues("http://10.0.2.2:8095", null, null, true));
        assertEquals("http://10.0.2.2:8095", ArumaRedirect.baseUrl());
    }

    @Test
    public void insecureFlagIsReadAsBooleanOrString() {
        assertTrue(ArumaRedirect.installFromValues("http://192.168.1.5:8080", "true", null, false));
        assertEquals("http://192.168.1.5:8080", ArumaRedirect.baseUrl());
    }

    @Test
    public void boolParsing() {
        assertTrue(ArumaRedirect.bool(Boolean.TRUE));
        assertTrue(ArumaRedirect.bool("TRUE"));
        assertTrue(ArumaRedirect.bool(" 1 "));
        assertTrue(ArumaRedirect.bool("yes"));
        assertFalse(ArumaRedirect.bool("false"));
        assertFalse(ArumaRedirect.bool("no"));
        assertFalse(ArumaRedirect.bool(null));
        assertFalse(ArumaRedirect.bool(Integer.valueOf(1)));
    }

    @Test
    public void factoryAlreadySetFailsClosedWithAClearMessage() {
        ArumaRedirect.resetForTest(new ArumaRedirect.Installer() {
            @Override
            public void install(URLStreamHandlerFactory factory) {
                throw new Error("factory already defined");
            }
        });
        try {
            ArumaRedirect.install("https://aruma.example.com");
            fail();
        } catch (ArumaRedirectException expected) {
            assertTrue(expected.getMessage().contains("URL.setURLStreamHandlerFactory"));
            assertTrue(expected.getMessage().contains("Requests would reach Google"));
        }
        assertNull(ArumaRedirect.baseUrl());
    }

    @Test
    public void reinstallingTheSameUrlIsIdempotentAndADifferentOneIsRefused() {
        ArumaRedirect.install("https://aruma.example.com");
        ArumaRedirect.install("https://aruma.example.com/");
        assertEquals(1, installs.get());
        try {
            ArumaRedirect.install("https://other.example.com");
            fail();
        } catch (ArumaRedirectException expected) {
            assertTrue(expected.getMessage().contains("already installed"));
        }
        assertEquals(1, installs.get());
    }
}
