// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.io.IOException;
import java.net.URL;
import org.junit.Test;

public class AuthEndpointsTest {
    private final ArumaBase base = new ArumaBase("https://aruma.example.com", false, false);

    private String rewrite(String url) throws IOException {
        return AuthEndpoints.rewrite(new URL(url), base);
    }

    private void refused(String url) throws Exception {
        try {
            rewrite(url);
            fail("expected refusal for " + url);
        } catch (IOException expected) {
            assertTrue(expected.getMessage().contains("ArumaRedirect refused"));
        }
    }

    @Test
    public void legacyRelyingPartyIsRewritten() throws Exception {
        assertEquals(
                "https://aruma.example.com/www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword?key=K",
                rewrite("https://www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword?key=K"));
    }

    @Test
    public void secureTokenIsRewritten() throws Exception {
        assertEquals("https://aruma.example.com/securetoken.googleapis.com/v1/token?key=K",
                rewrite("https://securetoken.googleapis.com/v1/token?key=K"));
    }

    @Test
    public void identityToolkitV1AndV2AreRewritten() throws Exception {
        assertEquals("https://aruma.example.com/identitytoolkit.googleapis.com/v1/accounts:signUp?key=K",
                rewrite("https://identitytoolkit.googleapis.com/v1/accounts:signUp?key=K"));
        assertEquals("https://aruma.example.com/identitytoolkit.googleapis.com/v2/recaptchaConfig?clientType=CLIENT_TYPE_ANDROID",
                rewrite("https://identitytoolkit.googleapis.com/v2/recaptchaConfig?clientType=CLIENT_TYPE_ANDROID"));
    }

    @Test
    public void queryIsKeptByteForByteAndAbsentQueryStaysAbsent() throws Exception {
        assertEquals("https://aruma.example.com/securetoken.googleapis.com/v1/token?a=%20b&c=d%2Fe",
                rewrite("https://securetoken.googleapis.com/v1/token?a=%20b&c=d%2Fe"));
        assertEquals("https://aruma.example.com/securetoken.googleapis.com/v1/token",
                rewrite("https://securetoken.googleapis.com/v1/token"));
    }

    @Test
    public void hostCaseTrailingDotAndExplicit443AreNormalised() throws Exception {
        assertEquals("https://aruma.example.com/securetoken.googleapis.com/v1/token",
                rewrite("https://SecureToken.GoogleAPIs.com/v1/token"));
        assertEquals("https://aruma.example.com/securetoken.googleapis.com/v1/token",
                rewrite("https://securetoken.googleapis.com./v1/token"));
        assertEquals("https://aruma.example.com/securetoken.googleapis.com/v1/token",
                rewrite("https://securetoken.googleapis.com:443/v1/token"));
    }

    @Test
    public void pathPrefixOfBaseIsKept() throws Exception {
        ArumaBase prefixed = new ArumaBase("https://example.com/aruma/", false, false);
        assertEquals("https://example.com/aruma/securetoken.googleapis.com/v1/token",
                AuthEndpoints.rewrite(new URL("https://securetoken.googleapis.com/v1/token"), prefixed));
    }

    @Test
    public void otherHostsAndPathsAreNotTouched() throws Exception {
        assertNull(rewrite("https://firebaseinstallations.googleapis.com/v1/projects/p/installations"));
        assertNull(rewrite("https://firestore.googleapis.com/google.firestore.v1.Firestore/Listen"));
        assertNull(rewrite("https://www.googleapis.com/oauth2/v4/token"));
        assertNull(rewrite("https://www.googleapis.com/identitytoolkit/v3/other"));
        assertNull(rewrite("https://example.com/identitytoolkit/v3/relyingparty/x"));
        assertNull(rewrite("https://evilsecuretoken.googleapis.com/v1/token"));
        assertNull(rewrite("https://securetoken.googleapis.com.evil.com/v1/token"));
        assertNull(rewrite("http://securetoken.googleapis.com/v1/token"));
    }

    @Test
    public void authRequestsInUnrewritableShapesAreRefusedNotPassedToGoogle() throws Exception {
        refused("https://securetoken.googleapis.com:8443/v1/token");
        refused("https://user:pw@securetoken.googleapis.com/v1/token");
        refused("https://securetoken.googleapis.com/v1/../../etc");
        refused("https://securetoken.googleapis.com/v1/%2e%2E/x");
        refused("https://www.googleapis.com/identitytoolkit/v3/relyingparty/../../../x");
    }

    @Test
    public void refusalMessageHasNoQuery() throws Exception {
        try {
            rewrite("https://securetoken.googleapis.com:8443/v1/token?key=SECRET");
            fail();
        } catch (IOException e) {
            assertFalse(e.getMessage().contains("SECRET"));
        }
    }
}
