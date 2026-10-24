// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import java.io.ByteArrayOutputStream;
import java.net.URI;
import java.net.URISyntaxException;
import java.nio.ByteBuffer;
import java.nio.charset.CharacterCodingException;
import java.nio.charset.CodingErrorAction;
import java.nio.charset.StandardCharsets;
import java.util.Locale;

/**
 * Cloud Storage download URLs between their forms (ADR-022 §16). The unified form, which Aruma serves from the root of
 * its host and every platform should produce, is
 * {@code https://<aruma-host>/v0/b/<bucket>/o/<encoded name>?alt=media&token=<uuid>}.
 */
public final class ArumaUrls {
    private static final String V0_BUCKET = "/v0/b/";

    private ArumaUrls() {}

    /**
     * Turns a download URL into the {@code gs://<bucket>/<name>} that {@code FirebaseStorage.getReferenceFromUrl}
     * accepts in every mode. That method takes a URL on firebase-storage's own base only: Google's when Storage is
     * redirected at run time, Aruma's (in lower case) when the app is built with {@code dev.aruma.storage}. A
     * {@code gs://} URL comes back unchanged.
     *
     * @throws IllegalArgumentException when {@code url} is not a download URL of that shape, including the prefixed
     *     form {@code <aruma>/firebasestorage.googleapis.com/v0/...}, which no platform reads.
     */
    public static String fromArumaUrl(String url) {
        if (url == null) {
            throw new IllegalArgumentException("not a Cloud Storage URL: null");
        }
        if (url.startsWith("gs://")) {
            return url;
        }
        URI u;
        try {
            u = new URI(url);
        } catch (URISyntaxException e) {
            throw notStorage(url);
        }
        String scheme = u.getScheme() == null ? "" : u.getScheme().toLowerCase(Locale.ROOT);
        String path = u.getRawPath();
        if (!(scheme.equals("https") || scheme.equals("http")) || u.getHost() == null || path == null
                || !path.startsWith(V0_BUCKET)) {
            throw notStorage(url);
        }
        String rest = path.substring(V0_BUCKET.length());
        int o = rest.indexOf("/o/");
        if (o <= 0 || o + 3 == rest.length()) {
            throw notStorage(url);
        }
        String bucket = decode(rest.substring(0, o), url);
        String name = decode(rest.substring(o + 3), url);
        if (bucket.contains("/")) {
            throw notStorage(url);
        }
        // The name as it is, decoded: getReferenceFromUrl encodes a gs:// path itself (Util.normalize,
        // Slashes.preserveSlashEncode), so an encoded name would be encoded twice and name another object.
        return "gs://" + bucket + "/" + name;
    }

    /**
     * Turns a download URL on Google's host, as {@code getDownloadUrl()} returns it when Storage is redirected at run
     * time, into the unified form on Aruma, so that it can be shown, stored or shared without reaching Google. Any
     * other URL comes back unchanged.
     *
     * @throws IllegalStateException when neither the plugin nor {@link ArumaRedirect} points Storage at Aruma.
     */
    public static String toArumaUrl(String url) {
        URI u;
        try {
            u = new URI(url);
        } catch (URISyntaxException e) {
            return url;
        }
        String host = u.getHost() == null ? "" : u.getHost().toLowerCase(Locale.ROOT);
        if (host.endsWith(".")) {
            host = host.substring(0, host.length() - 1);
        }
        if (!"https".equalsIgnoreCase(u.getScheme()) || !host.equals(AuthEndpoints.STORAGE_HOST)
                || (u.getPort() != -1 && u.getPort() != 443) || u.getRawUserInfo() != null
                || u.getRawPath() == null || !u.getRawPath().startsWith(V0_BUCKET)) {
            return url;
        }
        return origin() + u.getRawPath() + (u.getRawQuery() == null ? "" : "?" + u.getRawQuery());
    }

    /** Where Storage is served: the plugin's base without /v0, else ArumaURL, with the host in lower case. */
    private static String origin() {
        String built = StorageGuard.buildTimeBase();
        if (built != null && built.endsWith("/v0")) {
            return built.substring(0, built.length() - "/v0".length());
        }
        String base = ArumaRedirect.baseUrl();
        if (base == null) {
            throw new IllegalStateException("ArumaUrls.toArumaUrl: Storage is not pointed at Aruma (no ArumaURL, and "
                    + "the app was not built with dev.aruma.storage)");
        }
        int s = base.indexOf("://") + 3;
        int end = base.indexOf('/', s);
        String authority = end < 0 ? base.substring(s) : base.substring(s, end);
        return base.substring(0, s) + authority.toLowerCase(Locale.ROOT) + (end < 0 ? "" : base.substring(end));
    }

    private static IllegalArgumentException notStorage(String url) {
        return new IllegalArgumentException("not a Cloud Storage download URL (https://<host>/v0/b/<bucket>/o/<name>): "
                + url);
    }

    /** Strict percent-decoding of a path part, as UTF-8 (no '+' for space). */
    private static String decode(String s, String url) {
        ByteArrayOutputStream out = new ByteArrayOutputStream(s.length());
        for (int i = 0; i < s.length(); i++) {
            int c = s.codePointAt(i);
            if (c == '%') {
                if (i + 2 >= s.length()) {
                    throw notStorage(url);
                }
                int hi = Character.digit(s.charAt(i + 1), 16);
                int lo = Character.digit(s.charAt(i + 2), 16);
                if (hi < 0 || lo < 0) {
                    throw notStorage(url);
                }
                out.write(hi << 4 | lo);
                i += 2;
            } else {
                byte[] b = new String(Character.toChars(c)).getBytes(StandardCharsets.UTF_8);
                out.write(b, 0, b.length);
                i += Character.charCount(c) - 1;
            }
        }
        try {
            return StandardCharsets.UTF_8.newDecoder().onMalformedInput(CodingErrorAction.REPORT)
                    .onUnmappableCharacter(CodingErrorAction.REPORT).decode(ByteBuffer.wrap(out.toByteArray())).toString();
        } catch (CharacterCodingException e) {
            throw notStorage(url);
        }
    }
}
