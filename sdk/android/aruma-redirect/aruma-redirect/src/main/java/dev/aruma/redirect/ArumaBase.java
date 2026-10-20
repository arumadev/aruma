// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import java.net.URI;
import java.net.URISyntaxException;
import java.util.Locale;

/** The Aruma server requests are sent to: {@code scheme://host[:port][/prefix]}, without a trailing slash. */
final class ArumaBase {
    final String scheme;
    final String host;
    /** {@code scheme://authority[/prefix]}; never ends with a slash. */
    final String url;

    /**
     * @param allowInsecure accept http to any host (ArumaAllowInsecureURL).
     * @param debuggable the app is debuggable ({@code ApplicationInfo.FLAG_DEBUGGABLE}): http may then also go to the
     *     emulators' aliases of the development machine, which on a real device are ordinary private addresses.
     */
    ArumaBase(String raw, boolean allowInsecure, boolean debuggable) {
        String trimmed = raw == null ? "" : raw.trim();
        URI uri;
        try {
            uri = new URI(trimmed);
        } catch (URISyntaxException e) {
            throw invalid(trimmed);
        }
        String s = uri.getScheme() == null ? "" : uri.getScheme().toLowerCase(Locale.ROOT);
        String h = uri.getHost();
        if (!(s.equals("https") || s.equals("http")) || h == null || h.isEmpty()
                || uri.getRawQuery() != null || uri.getRawFragment() != null || uri.getRawUserInfo() != null
                || uri.getRawAuthority() == null || trimmed.contains("?") || trimmed.contains("#")) {
            throw invalid(trimmed);
        }
        if (s.equals("http") && !allowInsecure && !isLoopback(h) && !(debuggable && isEmulatorHost(h))) {
            throw new ArumaRedirectException("ArumaURL \"" + trimmed + "\" uses http to a host that is not loopback (or, "
                    + "in a debuggable app, an emulator's alias of the development machine); use https, or set "
                    + "ArumaAllowInsecureURL for development");
        }
        String path = uri.getRawPath() == null ? "" : uri.getRawPath();
        while (path.endsWith("/")) {
            path = path.substring(0, path.length() - 1);
        }
        scheme = s;
        host = h;
        url = s + "://" + uri.getRawAuthority() + path;
    }

    private static ArumaRedirectException invalid(String raw) {
        return new ArumaRedirectException("ArumaURL \"" + raw
                + "\" is not an absolute http(s) URL with a host and no query, fragment or credentials");
    }

    /** The aliases of the development machine in the Android emulator (10.0.2.2) and Genymotion (10.0.3.2). */
    static boolean isEmulatorHost(String host) {
        return host.equals("10.0.2.2") || host.equals("10.0.3.2");
    }

    /**
     * localhost, 127.0.0.0/8 and ::1. Names under .localhost are not included: RFC 6761 reserves them for loopback,
     * but Android's resolver does not guarantee that, so such a name could reach the network in plain text.
     */
    static boolean isLoopback(String host) {
        String h = host.toLowerCase(Locale.ROOT);
        if (h.startsWith("[") && h.endsWith("]")) {
            h = h.substring(1, h.length() - 1);
        }
        if (h.equals("localhost") || h.equals("::1")) {
            return true;
        }
        String[] octets = h.split("\\.", -1);
        if (octets.length != 4 || !octets[0].equals("127")) {
            return false;
        }
        for (String o : octets) {
            if (o.isEmpty() || o.length() > 3) {
                return false;
            }
            for (int i = 0; i < o.length(); i++) {
                if (o.charAt(i) < '0' || o.charAt(i) > '9') {
                    return false;
                }
            }
            if (Integer.parseInt(o) > 255) {
                return false;
            }
        }
        return true;
    }
}
