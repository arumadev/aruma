// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import org.junit.Test;

public class NetworkRequestVisitorTest {
    private static final String BASE = "https://aruma.example.com/v0";
    private static final String G = StorageBase.GOOGLE;

    private static String[] of(String... s) {
        return s;
    }

    @Test
    public void theOneLdcInTheStaticInitializerIsReplaced() {
        byte[] out = Classes.rewrite(Classes.networkRequest(of("unrelated", G), of("other")), BASE);
        // [Google's base anywhere, BASE in <clinit>]
        assertArrayEquals(new int[] {0, 1}, RewriteCheck.ldcCounts(out, BASE));
        assertTrue(RewriteCheck.indexOf(out, "unrelated".getBytes()) >= 0);
        assertTrue(RewriteCheck.indexOf(out, "other".getBytes()) >= 0);
    }

    @Test
    public void aMissingTextThrows() {
        throwsWith(Classes.networkRequest(of("https://firebasestorage.googleapis.com/v1"), of()), "found 0 there and 0 elsewhere");
        throwsWith(Classes.networkRequest(of(), of()), "found 0 there");
    }

    @Test
    public void theTextTwiceThrows() {
        throwsWith(Classes.networkRequest(of(G, G), of()), "found 2 there");
    }

    @Test
    public void theTextOutsideTheStaticInitializerThrows() {
        throwsWith(Classes.networkRequest(of(G), of(G)), "found 1 there and 1 elsewhere");
        throwsWith(Classes.networkRequest(of(), of(G)), "found 0 there and 1 elsewhere");
    }

    private static void throwsWith(byte[] in, String detail) {
        try {
            Classes.rewrite(in, BASE);
            fail("expected the visitor to throw (" + detail + ")");
        } catch (IllegalStateException e) {
            assertTrue(e.getMessage(), e.getMessage().contains(detail));
            assertTrue(e.getMessage(), e.getMessage().contains("no request goes to Google"));
        }
    }
}
