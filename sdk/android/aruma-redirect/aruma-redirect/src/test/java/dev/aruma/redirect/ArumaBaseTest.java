// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import org.junit.Test;

public class ArumaBaseTest {
    private static void rejected(String raw, boolean allowInsecure) {
        rejected(raw, allowInsecure, false);
    }

    private static void rejected(String raw, boolean allowInsecure, boolean debuggable) {
        try {
            new ArumaBase(raw, allowInsecure, debuggable);
            fail("expected rejection of " + raw);
        } catch (ArumaRedirectException expected) {
            assertTrue(expected.getMessage().contains("ArumaURL"));
        }
    }

    @Test
    public void httpsIsAcceptedAndTrailingSlashesDropped() {
        assertEquals("https://aruma.example.com", new ArumaBase(" https://aruma.example.com/ ", false, false).url);
        assertEquals("https://aruma.example.com:8443/p/q", new ArumaBase("https://aruma.example.com:8443/p/q//", false, false).url);
        assertEquals("https", new ArumaBase("HTTPS://aruma.example.com", false, false).scheme);
    }

    @Test
    public void httpIsAcceptedOnlyForLoopback() {
        assertEquals("http://localhost:8080", new ArumaBase("http://localhost:8080", false, false).url);
        assertEquals("http://127.0.0.1:8080", new ArumaBase("http://127.0.0.1:8080", false, false).url);
        assertEquals("http://[::1]:8080", new ArumaBase("http://[::1]:8080", false, false).url);
        rejected("http://aruma.example.com", false);
        rejected("http://192.168.1.5:8080", false);
        rejected("http://127.0.0.1.evil.com", false);
        rejected("http://127.0.0.256", false);
        // RFC 6761 reserves .localhost for loopback, but Android's resolver does not promise it.
        rejected("http://aruma.localhost:8080", false);
        rejected("http://aruma.localhost:8080", false, true);
    }

    @Test
    public void emulatorHostsNeedADebuggableAppOrTheOptIn() {
        // On a real device 10.0.2.2 and 10.0.3.2 are ordinary private addresses.
        rejected("http://10.0.2.2:8095", false, false);
        rejected("http://10.0.3.2:8080", false, false);
        assertEquals("http://10.0.2.2:8095", new ArumaBase("http://10.0.2.2:8095", false, true).url);
        assertEquals("http://10.0.3.2:8080", new ArumaBase("http://10.0.3.2:8080", false, true).url);
        assertEquals("http://10.0.2.2:8095", new ArumaBase("http://10.0.2.2:8095", true, false).url);
        // Debuggable widens nothing else.
        rejected("http://10.0.2.3:8095", false, true);
        rejected("http://192.168.1.5:8080", false, true);
    }

    @Test
    public void allowInsecureAcceptsHttpToAnyHost() {
        assertEquals("http://192.168.1.5:8080", new ArumaBase("http://192.168.1.5:8080", true, false).url);
    }

    @Test
    public void malformedUrlsAreRejected() {
        rejected("", false);
        rejected("   ", false);
        rejected("aruma.example.com", false);
        rejected("ftp://aruma.example.com", false);
        rejected("https://", false);
        rejected("https:///path", false);
        rejected("https://aruma.example.com?x=1", false);
        rejected("https://aruma.example.com/?", false);
        rejected("https://aruma.example.com#frag", false);
        rejected("https://user:pw@aruma.example.com", false);
        rejected("https://aruma .example.com", false);
    }

    @Test
    public void loopbackPredicate() {
        assertTrue(ArumaBase.isLoopback("LOCALHOST"));
        assertTrue(ArumaBase.isLoopback("127.255.0.1"));
        assertTrue(ArumaBase.isLoopback("[::1]"));
        assertFalse(ArumaBase.isLoopback("128.0.0.1"));
        assertFalse(ArumaBase.isLoopback("10.0.2.2"));
        assertFalse(ArumaBase.isLoopback("app.localhost"));
        assertTrue(ArumaBase.isEmulatorHost("10.0.2.2"));
        assertFalse(ArumaBase.isEmulatorHost("10.0.2.3"));
    }
}
