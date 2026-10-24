// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import java.net.URI;
import java.net.URISyntaxException;
import java.util.Locale;

/**
 * The text firebase-storage gets in place of {@link #GOOGLE}: {@code <scheme>://<host>[:port]/v0}, the root of the
 * Aruma server. Every download URL the library builds then has the unified form of ADR-022 §16,
 * {@code https://<aruma-host>/v0/b/<bucket>/o/<path>?alt=media&token=<uuid>}, so the URL is written as Aruma serves
 * it:
 *
 * <ul>
 *   <li>The host is lower case: {@code Util.normalize} (refFromURL) compares the lower-cased authority of a URL with
 *       the base's authority as it is, so a base in upper case would refuse the library's own URLs.
 *   <li>The scheme's default port is dropped, so no URL carries {@code :443}.
 *   <li>No path: Aruma serves {@code /v0} from the root of its host. The prefixed form
 *       {@code <aruma>/firebasestorage.googleapis.com/v0} is never written into a URL (ADR-022 §16).
 * </ul>
 */
final class StorageBase {
    /** The text in {@code NetworkRequest.<clinit>} that becomes {@code PROD_BASE_URL} (firebase-storage 23). */
    static final String GOOGLE = "https://firebasestorage.googleapis.com/v0";
    static final String GOOGLE_HOST = "firebasestorage.googleapis.com";

    private StorageBase() {}

    /**
     * Returns the base for {@code raw}, the Aruma server's URL as the developer wrote it.
     *
     * @param allowInsecure accept http to any host. Development only.
     * @param debuggable the variant is debuggable: http may then also go to the emulators' aliases of the development
     *     machine (10.0.2.2, 10.0.3.2), as aruma-redirect accepts them.
     * @throws IllegalArgumentException with a message for the build log.
     */
    static String of(String raw, boolean allowInsecure, boolean debuggable) {
        String trimmed = raw == null ? "" : raw.trim();
        if (trimmed.isEmpty()) {
            throw new IllegalArgumentException("set the Aruma server's URL: arumaStorage { url = \"https://aruma.example.com\" }");
        }
        URI uri;
        try {
            uri = new URI(trimmed);
        } catch (URISyntaxException e) {
            throw invalid(trimmed, "it is not a URL");
        }
        String scheme = uri.getScheme() == null ? "" : uri.getScheme().toLowerCase(Locale.ROOT);
        if (!scheme.equals("https") && !scheme.equals("http")) {
            throw invalid(trimmed, "use https (or http for development)");
        }
        if (uri.getRawUserInfo() != null || uri.getRawQuery() != null || uri.getRawFragment() != null
                || trimmed.contains("?") || trimmed.contains("#")) {
            throw invalid(trimmed, "no credentials, query or fragment");
        }
        String host = uri.getHost();
        if (host == null || host.isEmpty()) {
            throw invalid(trimmed, "it has no host (write a non-ASCII name in its xn-- form)");
        }
        for (int i = 0; i < host.length(); i++) {
            if (host.charAt(i) > 0x7e) {
                throw invalid(trimmed, "write the host in its xn-- form");
            }
        }
        host = host.toLowerCase(Locale.ROOT);
        if (host.endsWith(".")) {
            throw invalid(trimmed, "no trailing dot in the host");
        }
        String path = uri.getRawPath() == null ? "" : uri.getRawPath();
        if (!path.isEmpty() && !path.equals("/")) {
            throw invalid(trimmed, "Aruma serves Cloud Storage from the root of its host; give the URL without a path");
        }
        if (host.equals(GOOGLE_HOST) || host.endsWith("." + GOOGLE_HOST) || host.endsWith(".googleapis.com")) {
            throw invalid(trimmed, "it is Google's host, not an Aruma server");
        }
        if (scheme.equals("http") && !allowInsecure && !isLoopback(host) && !(debuggable && isEmulatorHost(host))) {
            throw invalid(trimmed, "http only to a loopback address (or, in a debuggable variant, 10.0.2.2 or 10.0.3.2); "
                    + "use https, or set arumaStorage { allowInsecure = true } for development");
        }
        int port = uri.getPort();
        boolean defaultPort = port == -1 || (scheme.equals("https") && port == 443) || (scheme.equals("http") && port == 80);
        return scheme + "://" + host + (defaultPort ? "" : ":" + port) + "/v0";
    }

    private static IllegalArgumentException invalid(String raw, String why) {
        return new IllegalArgumentException("arumaStorage.url \"" + raw + "\" cannot be used: " + why);
    }

    /** The aliases of the development machine in the Android emulator (10.0.2.2) and Genymotion (10.0.3.2). */
    static boolean isEmulatorHost(String host) {
        return host.equals("10.0.2.2") || host.equals("10.0.3.2");
    }

    /** localhost, 127.0.0.0/8 and ::1, as aruma-redirect accepts them. */
    static boolean isLoopback(String host) {
        if (host.equals("localhost") || host.equals("[::1]") || host.equals("::1")) {
            return true;
        }
        String[] octets = host.split("\\.", -1);
        if (octets.length != 4 || !octets[0].equals("127")) {
            return false;
        }
        for (String o : octets) {
            if (o.isEmpty() || o.length() > 3 || !o.chars().allMatch(c -> c >= '0' && c <= '9') || Integer.parseInt(o) > 255) {
                return false;
            }
        }
        return true;
    }
}
