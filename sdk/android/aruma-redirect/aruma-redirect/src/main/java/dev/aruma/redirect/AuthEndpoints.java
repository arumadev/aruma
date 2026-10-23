// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import java.io.IOException;
import java.net.URL;
import java.util.Locale;

/**
 * The Firebase Auth endpoint families and how each maps onto an Aruma server. Aruma serves Google's paths under
 * its own host, so a redirect only adds the original host as the first path segment:
 * {@code https://<google host><path>?<query>} becomes {@code <base>/<google host><path>?<query>}.
 */
final class AuthEndpoints {
    static final String IDENTITY_TOOLKIT_HOST = "identitytoolkit.googleapis.com";
    static final String SECURE_TOKEN_HOST = "securetoken.googleapis.com";
    static final String LEGACY_HOST = "www.googleapis.com";
    static final String LEGACY_PATH_PREFIX = "/identitytoolkit/v3/relyingparty/";
    /** SPIKE (S0-B, docs/research/storage-production-path.md): Cloud Storage for Firebase, off by default. */
    static final String STORAGE_HOST = "firebasestorage.googleapis.com";
    static volatile boolean storageSpike;

    private AuthEndpoints() {}

    /**
     * Returns the Aruma URL for an Auth request, or null when {@code url} is not an Auth endpoint (it is then
     * passed through untouched). Throws when it is an Auth endpoint in a shape that cannot be rewritten safely
     * (explicit port, credentials, dot segments): refusing is safer than letting the request reach Google.
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
                if (!storageSpike) {
                    return null;
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
