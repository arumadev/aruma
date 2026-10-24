// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.net.URLStreamHandlerFactory;
import org.junit.After;
import org.junit.Test;

public class ArumaUrlsTest {
    private static final String TOKEN = "alt=media&token=0b1c2d3e-0000-4000-8000-000000000001";

    @After
    public void tearDown() {
        StorageGuard.resetForTest(StorageGuard.REFLECTION);
        ArumaRedirect.resetForTest(new ArumaRedirect.Installer() {
            @Override
            public void install(URLStreamHandlerFactory factory) {}
        });
    }

    @Test
    public void everyDownloadUrlBecomesAGsUrl() {
        String[][] cases = {
            // The unified form, on any host and scheme.
            {"https://aruma.example.com/v0/b/p.aruma.dev/o/users%2Fu1%2Fa.txt?" + TOKEN, "gs://p.aruma.dev/users/u1/a.txt"},
            {"http://127.0.0.1:9430/v0/b/p.aruma.dev/o/a.txt", "gs://p.aruma.dev/a.txt"},
            // Google's, and iOS's with :443.
            {"https://firebasestorage.googleapis.com/v0/b/p.appspot.com/o/a%2Fb?" + TOKEN, "gs://p.appspot.com/a/b"},
            {"https://firebasestorage.googleapis.com:443/v0/b/p.appspot.com/o/a", "gs://p.appspot.com/a"},
            // Names getReferenceFromUrl would otherwise misread: Uri.getPath() decodes.
            {"https://a.example/v0/b/b/o/100%25%20sure%3F%23x", "gs://b/100%25%20sure%3F%23x"},
            {"https://a.example/v0/b/b/o/%C3%A9t%C3%A9+", "gs://b/%C3%A9t%C3%A9%2B"},
            {"gs://b/already", "gs://b/already"},
        };
        for (String[] c : cases) {
            assertEquals(c[0], c[1], ArumaUrls.fromArumaUrl(c[0]));
        }
    }

    @Test
    public void otherShapesFailExplicitly() {
        String[] bad = {
            null,
            "",
            "not a url",
            "ftp://a.example/v0/b/b/o/x",
            // The prefixed form: no platform reads it (ADR-022 §16).
            "https://aruma.example.com/firebasestorage.googleapis.com/v0/b/b/o/x",
            "https://a.example/v0/b/b/o/",
            "https://a.example/v0/b/b/o",
            "https://a.example/v0/b//o/x",
            "https://a.example/v0/b/b%2Fc/o/x",
            "https://a.example/v0/b/b/o/%zz",
            "https://a.example/v0/b/b/o/%C3",
            "https://a.example/b/b/o/x",
        };
        for (String url : bad) {
            try {
                ArumaUrls.fromArumaUrl(url);
                fail("expected a refusal for " + url);
            } catch (IllegalArgumentException expected) {
                assertTrue(expected.getMessage().contains("not a Cloud Storage"));
            }
        }
    }

    @Test
    public void googleLinksMoveToArumaInTheUnifiedForm() {
        assertTrue(ArumaRedirect.installFromValues("https://Aruma.Example.com", null, null, false));
        assertEquals("https://aruma.example.com/v0/b/p/o/a%2Fb?" + TOKEN,
                ArumaUrls.toArumaUrl("https://firebasestorage.googleapis.com/v0/b/p/o/a%2Fb?" + TOKEN));
        assertEquals("https://aruma.example.com/v0/b/p/o/a",
                ArumaUrls.toArumaUrl("https://FIREBASESTORAGE.googleapis.com.:443/v0/b/p/o/a"));
        // Anything else is left as it is.
        for (String other : new String[] {
                "https://aruma.example.com/v0/b/p/o/a",
                "https://firebasestorage.googleapis.com:8443/v0/b/p/o/a",
                "http://firebasestorage.googleapis.com/v0/b/p/o/a",
                "https://firebasestorage.googleapis.com/v1/x",
                "https://example.com/v0/b/p/o/a"}) {
            assertEquals(other, ArumaUrls.toArumaUrl(other));
        }
    }

    @Test
    public void thePluginsBaseWinsOverArumaUrl() {
        StorageGuard.resetForTest(new StorageGuard.Probe() {
            @Override
            public String prodBaseUrl() {
                return "https://storage.example.com/v0";
            }
        });
        assertTrue(ArumaRedirect.installFromValues("https://aruma.example.com", null, null, false));
        assertEquals("https://storage.example.com/v0/b/p/o/a",
                ArumaUrls.toArumaUrl("https://firebasestorage.googleapis.com/v0/b/p/o/a"));
    }

    @Test
    public void withoutArumaThereIsNoUnifiedForm() {
        try {
            ArumaUrls.toArumaUrl("https://firebasestorage.googleapis.com/v0/b/p/o/a");
            fail();
        } catch (IllegalStateException expected) {
            assertTrue(expected.getMessage().contains("not pointed at Aruma"));
        }
    }
}
