// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import java.net.URI;
import java.net.URISyntaxException;
import java.util.Locale;

/**
 * The run-time layer of the closed failure for Cloud Storage (ADR-022 §16.2). firebase-storage builds every request and
 * download URL from the public {@code NetworkRequest.PROD_BASE_URL}. The Gradle plugin {@code dev.aruma.storage}
 * points it at Aruma when the app is built; without the plugin it is still Google's, and only the run-time redirect
 * of {@code firebasestorage.googleapis.com} keeps the requests off Google. So when the redirect is installed:
 *
 * <ul>
 *   <li>firebase-storage absent: nothing to check.
 *   <li>{@code PROD_BASE_URL} not on Google's host: the app was built with the plugin.
 *   <li>On Google's host, and Storage redirected at run time: the fallback. It works, but download URLs stay on
 *       Google's host, so the log says so.
 *   <li>On Google's host and Storage not redirected ({@code ArumaStorageRedirect} false): the app stops here.
 * </ul>
 */
final class StorageGuard {
    static final String NETWORK_REQUEST = "com.google.firebase.storage.network.NetworkRequest";

    /** Reads {@code NetworkRequest.PROD_BASE_URL}. A test seam. */
    interface Probe {
        /** @return the field as text, or null when firebase-storage is not in the app. */
        String prodBaseUrl() throws ReflectiveOperationException;
    }

    static final Probe REFLECTION = new Probe() {
        @Override
        public String prodBaseUrl() throws ReflectiveOperationException {
            Class<?> c;
            try {
                c = Class.forName(NETWORK_REQUEST, true, StorageGuard.class.getClassLoader());
            } catch (ClassNotFoundException e) {
                return null;
            }
            Object v = c.getField("PROD_BASE_URL").get(null);
            return v == null ? "" : v.toString();
        }
    };

    static volatile Probe probe = REFLECTION;
    /** {@code PROD_BASE_URL} when the plugin rewrote it, else null. Set by {@link #check}. */
    private static volatile String buildTimeBase;

    private StorageGuard() {}

    /**
     * Checks where firebase-storage sends its requests.
     *
     * @param runtimeRedirect whether {@code firebasestorage.googleapis.com} is redirected to Aruma at run time.
     * @return a line for the log, or null when firebase-storage is not in the app.
     * @throws ArumaRedirectException when Storage requests would reach Google.
     */
    static String check(boolean runtimeRedirect) {
        String base;
        try {
            base = probe.prodBaseUrl();
        } catch (ReflectiveOperationException | LinkageError | RuntimeException e) {
            if (runtimeRedirect) {
                return "Firebase Storage: cannot read " + NETWORK_REQUEST + ".PROD_BASE_URL (" + e
                        + "); its requests to " + AuthEndpoints.STORAGE_HOST + " are redirected at run time";
            }
            throw new ArumaRedirectException("ArumaRedirect cannot tell where Firebase Storage sends its requests ("
                    + NETWORK_REQUEST + ".PROD_BASE_URL: " + e + "), and ArumaStorageRedirect is false. The app stops "
                    + "here so that no Storage request goes to Google.", e);
        }
        if (base == null) {
            buildTimeBase = null;
            return null;
        }
        String host = host(base);
        if (host != null && !host.equals(AuthEndpoints.STORAGE_HOST)) {
            buildTimeBase = base;
            return "Firebase Storage: requests and download URLs go to " + base + " (built with dev.aruma.storage)";
        }
        buildTimeBase = null;
        if (!runtimeRedirect) {
            throw new ArumaRedirectException("Firebase Storage would send its requests to Google: this app was built "
                    + "without the Aruma Gradle plugin dev.aruma.storage (" + NETWORK_REQUEST + ".PROD_BASE_URL is "
                    + base + ") and ArumaStorageRedirect is false, so nothing redirects them. Apply the plugin "
                    + "(id(\"dev.aruma.storage\") with arumaStorage { url = ... }), or remove ArumaStorageRedirect=false "
                    + "to redirect Storage at run time.");
        }
        return "Firebase Storage: redirected at run time (fallback); getDownloadUrl() still returns links on "
                + AuthEndpoints.STORAGE_HOST + ". Apply the Gradle plugin dev.aruma.storage to build them on Aruma";
    }

    /** The Aruma base firebase-storage was built with ({@code <scheme>://<host>[:port]/v0}), or null. */
    static String buildTimeBase() {
        return buildTimeBase;
    }

    private static String host(String url) {
        try {
            String h = new URI(url).getHost();
            return h == null ? null : h.toLowerCase(Locale.ROOT);
        } catch (URISyntaxException e) {
            return null;
        }
    }

    /** Test hook. */
    static void resetForTest(Probe replacement) {
        probe = replacement;
        buildTimeBase = null;
    }
}
