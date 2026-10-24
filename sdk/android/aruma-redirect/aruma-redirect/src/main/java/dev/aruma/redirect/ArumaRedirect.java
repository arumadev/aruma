// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import android.util.Log;

import java.net.URL;

/**
 * Sends the official Firebase Auth SDK for Android to an Aruma server, in its normal production mode (no emulator
 * settings). The SDK opens its requests with {@link URL#openConnection()}; this installs a process-wide
 * {@link java.net.URLStreamHandlerFactory} that rewrites the Auth hosts to the Aruma server and leaves every other URL
 * to the platform.
 *
 * <p>Cloud Storage for Firebase goes to Aruma through the Gradle plugin {@code dev.aruma.storage}, at build time. The
 * factory also redirects {@code firebasestorage.googleapis.com}, as a fallback for an app built without the plugin,
 * and at install checks which of the two is in place ({@link StorageGuard}): an app with neither stops at launch.
 *
 * <p>Normally nothing is called: {@link ArumaRedirectProvider} reads the {@code ArumaURL} meta-data at process start.
 * Apps that prefer code call {@link #install(String)} before Firebase sends its first request.
 */
public final class ArumaRedirect {
    /** Manifest meta-data names, in {@code <application>}. */
    public static final String META_URL = "ArumaURL";
    /** Boolean: accept http to any host. Development only. */
    public static final String META_ALLOW_INSECURE = "ArumaAllowInsecureURL";
    /** Boolean: log host and path of each redirected request (never query, headers or body). */
    public static final String META_LOG = "ArumaRedirectLog";
    /**
     * Boolean, default true: also redirect Cloud Storage's {@code firebasestorage.googleapis.com} at run time. Set it
     * to false in an app built with {@code dev.aruma.storage} to make the plugin mandatory: without it, the app then
     * stops at launch instead of falling back.
     */
    public static final String META_STORAGE_REDIRECT = "ArumaStorageRedirect";

    private static final String TAG = "ArumaRedirect";

    /** Test seam for {@code URL.setURLStreamHandlerFactory}. */
    interface Installer {
        void install(java.net.URLStreamHandlerFactory factory);
    }

    private static final Object LOCK = new Object();
    private static Installer installer = new Installer() {
        @Override
        public void install(java.net.URLStreamHandlerFactory factory) {
            URL.setURLStreamHandlerFactory(factory);
        }
    };
    private static String baseUrl;

    private ArumaRedirect() {}

    /** Redirects Firebase Auth requests to {@code url}. http is accepted only for loopback hosts. */
    public static void install(String url) {
        install(url, false, false);
    }

    /**
     * Installs from code. http is accepted for loopback hosts; for the emulator's alias of the development machine
     * ({@code 10.0.2.2}) pass {@code allowInsecure}, for example {@code BuildConfig.DEBUG}. (The manifest route
     * accepts that alias by itself in a debuggable app.)
     *
     * @param allowInsecure accept http to any host. Development only.
     * @param logRequests log host and path of each redirected request.
     * @throws ArumaRedirectException when the URL is refused or the factory cannot be installed. Nothing is installed.
     */
    public static void install(String url, boolean allowInsecure, boolean logRequests) {
        install(new ArumaBase(url, allowInsecure, false), logRequests, true);
    }

    private static void install(ArumaBase base, boolean logRequests, boolean storageRedirect) {
        synchronized (LOCK) {
            if (baseUrl != null) {
                if (baseUrl.equals(base.url)) {
                    return;
                }
                throw new ArumaRedirectException("ArumaRedirect is already installed for " + baseUrl
                        + "; it cannot be changed to " + base.url + " in the same process");
            }
            // Before anything is installed: a Storage that would reach Google stops the app here.
            String storage = StorageGuard.check(storageRedirect);
            final URL context;
            try {
                context = new URL("https://aruma.invalid/");
            } catch (java.net.MalformedURLException e) {
                throw new ArumaRedirectException("ArumaRedirect could not obtain the platform https handler", e);
            }
            ArumaStreamHandlerFactory.RequestLog log = logRequests ? new ArumaStreamHandlerFactory.RequestLog() {
                @Override
                public void redirected(String originalHost, String path) {
                    info("redirected " + originalHost + path);
                }
            } : null;
            try {
                installer.install(new ArumaStreamHandlerFactory(base, context, log));
            } catch (Error e) {
                throw new ArumaRedirectException("ArumaRedirect cannot redirect Firebase Auth to " + base.url
                        + ": another component already called URL.setURLStreamHandlerFactory in this process ("
                        + e.getMessage() + "). Requests would reach Google, so the app stops here. Remove the "
                        + "other factory or install ArumaRedirect from it.", e);
            }
            AuthEndpoints.storageRedirect = storageRedirect;
            baseUrl = base.url;
            info("installed: Firebase Auth requests go to " + base.url
                    + (storageRedirect ? ", and so do requests to " + AuthEndpoints.STORAGE_HOST : ""));
            if (storage != null) {
                info(storage);
            }
        }
    }

    /**
     * Installs from meta-data values (as read from the app manifest). A null or blank {@code url} does nothing and
     * returns false; a present but invalid value throws. {@code debuggable} is the app's FLAG_DEBUGGABLE: only then
     * may http go to an emulator's alias of the development machine without ArumaAllowInsecureURL.
     */
    static boolean installFromValues(Object url, Object allowInsecure, Object logRequests, boolean debuggable) {
        return installFromValues(url, allowInsecure, logRequests, null, debuggable);
    }

    /** As above, with ArumaStorageRedirect: absent is true. */
    static boolean installFromValues(Object url, Object allowInsecure, Object logRequests, Object storageRedirect,
            boolean debuggable) {
        if (url == null || url.toString().trim().isEmpty()) {
            return false;
        }
        install(new ArumaBase(url.toString(), bool(allowInsecure), debuggable), bool(logRequests),
                storageRedirect == null || bool(storageRedirect));
        return true;
    }

    /** The Aruma server Auth requests go to, or null when the redirect is not active. */
    public static String baseUrl() {
        synchronized (LOCK) {
            return baseUrl;
        }
    }

    static boolean bool(Object v) {
        if (v instanceof Boolean) {
            return (Boolean) v;
        }
        if (v instanceof String) {
            String s = ((String) v).trim().toLowerCase(java.util.Locale.ROOT);
            return s.equals("true") || s.equals("1") || s.equals("yes");
        }
        return false;
    }

    /**
     * The S0-B spike's switch, kept for test/experiments/storage-path: the Storage redirect is now on by default
     * ({@link #META_STORAGE_REDIRECT}), and the build-time plugin {@code dev.aruma.storage} is the production path.
     * Read per request; this skips the launch check.
     *
     * @deprecated use the manifest's {@code ArumaStorageRedirect}, or the Gradle plugin.
     */
    @Deprecated
    public static void setStorageSpike(boolean on) {
        AuthEndpoints.storageRedirect = on;
    }

    /** Test hook: forget the installed state and swap the installer. */
    static void resetForTest(Installer replacement) {
        synchronized (LOCK) {
            baseUrl = null;
            installer = replacement;
            AuthEndpoints.storageRedirect = true;
        }
    }

    private static void info(String message) {
        try {
            Log.i(TAG, message);
        } catch (RuntimeException ignored) {
            // not on Android (JVM unit tests)
        }
    }
}
