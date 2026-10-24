// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import org.junit.Test;

public class StorageBaseTest {
    @Test
    public void theBaseIsTheUnifiedRoot() {
        String[][] cases = {
            {"https://aruma.example.com", "https://aruma.example.com/v0"},
            {"https://aruma.example.com/", "https://aruma.example.com/v0"},
            {"  https://aruma.example.com  ", "https://aruma.example.com/v0"},
            // Lower case, as Util.normalize compares it (ADR-022 §16.2).
            {"HTTPS://Aruma.EXAMPLE.com", "https://aruma.example.com/v0"},
            // No :443 in any URL (ADR-022 §16).
            {"https://aruma.example.com:443", "https://aruma.example.com/v0"},
            {"https://aruma.example.com:8443", "https://aruma.example.com:8443/v0"},
            {"http://127.0.0.1:9430", "http://127.0.0.1:9430/v0"},
            {"http://LocalHost:9430/", "http://localhost:9430/v0"},
            {"http://localhost:80", "http://localhost/v0"},
            {"http://[::1]:9430", "http://[::1]:9430/v0"},
        };
        for (String[] c : cases) {
            assertEquals(c[0], c[1], StorageBase.of(c[0], false, false));
        }
    }

    @Test
    public void httpNeedsLoopbackADebuggableEmulatorAliasOrTheFlag() {
        refused("http://aruma.example.com", false, false, "allowInsecure");
        refused("http://10.0.2.2:9430", false, false, "allowInsecure");
        assertEquals("http://10.0.2.2:9430/v0", StorageBase.of("http://10.0.2.2:9430", false, true));
        assertEquals("http://192.168.1.5:8080/v0", StorageBase.of("http://192.168.1.5:8080", true, false));
        refused("http://127.0.0.1.example.com", false, false, "allowInsecure");
    }

    @Test
    public void unusableUrlsAreRefused() {
        refused(null, false, false, "set the Aruma server's URL");
        refused(" ", false, false, "set the Aruma server's URL");
        refused("aruma.example.com", false, false, "use https");
        refused("ftp://aruma.example.com", false, false, "use https");
        refused("https://user:pw@aruma.example.com", false, false, "credentials");
        refused("https://aruma.example.com/?a=b", false, false, "query");
        refused("https://aruma.example.com/#x", false, false, "fragment");
        refused("https://aruma.example.com/aruma", false, false, "without a path");
        refused("https://aruma.example.com/firebasestorage.googleapis.com", false, false, "without a path");
        refused("https://firebasestorage.googleapis.com", false, false, "Google's host");
        refused("https://FirebaseStorage.googleapis.com:443/", false, false, "Google's host");
        refused("https://aruma.example.com.", false, false, "trailing dot");
        refused("https://bad_host.example.com", false, false, "no host");
        refused("https://ärüma.example.com", false, false, "xn--");
    }

    private static void refused(String raw, boolean insecure, boolean debuggable, String detail) {
        try {
            StorageBase.of(raw, insecure, debuggable);
            fail("expected a refusal for " + raw);
        } catch (IllegalArgumentException e) {
            assertTrue(raw + ": " + e.getMessage(), e.getMessage().contains(detail));
        }
    }
}
