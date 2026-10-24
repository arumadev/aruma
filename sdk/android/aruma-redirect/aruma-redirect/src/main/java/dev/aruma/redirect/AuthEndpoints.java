// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import java.io.IOException;
import java.net.URL;
import java.util.Locale;

/**
 * The Firebase endpoint families (Auth, and Cloud Storage for Firebase) and how each maps onto an Aruma server. Aruma
 * serves Google's paths under its own host, so a redirect only adds the original host as the first path segment:
 * {@code https://<google host><path>?<query>} becomes {@code <base>/<google host><path>?<query>}.
 */
final class AuthEndpoints {
    static final String IDENTITY_TOOLKIT_HOST = "identitytoolkit.googleapis.com";
    static final String SECURE_TOKEN_HOST = "securetoken.googleapis.com";
    static final String LEGACY_HOST = "www.googleapis.com";
    static final String LEGACY_PATH_PREFIX = "/identitytoolkit/v3/relyingparty/";
    /**
     * Cloud Storage for Firebase: the run-time fallback of ADR-022 §16.2, on unless ArumaStorageRedirect is false. An
     * app built with dev.aruma.storage sends nothing to this host, so the redirect only sees what that build missed
     * (an old download URL on Google's host, for one). With the fallback off such a request is refused, as on iOS,
     * never passed to Google with its token.
     */
    static final String STORAGE_HOST = "firebasestorage.googleapis.com";
    static volatile boolean storageRedirect = true;

    private AuthEndpoints() {}

    /**
     * Returns the Aruma URL for a request to one of these endpoints, or null when {@code url} is not one (it is then
     * passed through untouched). Throws when it is one in a shape that cannot be rewritten safely
     * (explicit port, credentials, dot segments), or a Storage request while the Storage redirect is off: refusing is
     * safer than letting the request reach Google.
     */
    static String rewrite(URL url, ArumaBase base) throws IOException {
        if (!"https".equalsIgnoreCase(url.getProtocol())) {
            return null;
        }
        String host = url.getHost();
        if (host == null) {
            return null;
        }
        host = host.toLowerCase(Locale.ROOT);
        if (host.endsWith(".")) {
            host = host.substring(0, host.length() - 1);
        }
        String path = url.getPath() == null ? "" : url.getPath();
        switch (host) {
            case IDENTITY_TOOLKIT_HOST:
            case SECURE_TOKEN_HOST:
                break;
            case LEGACY_HOST:
                if (!path.startsWith(LEGACY_PATH_PREFIX)) {
                    return null;
                }
                break;
            case STORAGE_HOST:
                if (!storageRedirect) {
                    throw refused(host, "ArumaStorageRedirect is false, so Cloud Storage requests to Google are refused");
                }
                break;
            default:
                return null;
        }
        if ((url.getPort() != -1 && url.getPort() != 443) || url.getUserInfo() != null) {
            throw refused(host, "port or credentials in the URL");
        }
        if (hasDotSegment(path) || path.indexOf('\\') >= 0) {
            throw refused(host, "dot segments in the path");
        }
        String query = url.getQuery();
        return base.url + "/" + host + path + (query == null ? "" : "?" + query);
    }

    private static boolean hasDotSegment(String path) {
        for (String seg : path.split("/", -1)) {
            String s = seg.replace("%2e", ".").replace("%2E", ".");
            if (s.equals(".") || s.equals("..")) {
                return true;
            }
        }
        return false;
    }

    private static IOException refused(String host, String why) {
        return new IOException("ArumaRedirect refused a request to " + host + ": " + why);
    }
}
