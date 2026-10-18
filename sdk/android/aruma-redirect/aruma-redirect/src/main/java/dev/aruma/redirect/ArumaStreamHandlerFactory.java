// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import java.io.IOException;
import java.net.Proxy;
import java.net.URL;
import java.net.URLConnection;
import java.net.URLStreamHandler;
import java.net.URLStreamHandlerFactory;

/**
 * Handles {@code https}: Firebase Auth requests go to Aruma, every other https URL is opened by the platform's own
 * handler. The platform handler is obtained through public API only: {@code platformContext} is a URL created before
 * the factory was installed, so it carries the platform handler, and {@code new URL(platformContext, spec)} reuses
 * it for an https spec. Other protocols return null from the factory and keep the platform defaults.
 */
final class ArumaStreamHandlerFactory implements URLStreamHandlerFactory {
    /** Receives host and path of each redirected request (never query, headers or body). May be null. */
    interface RequestLog {
        void redirected(String originalHost, String path);
    }

    private final ArumaBase base;
    private final URL platformContext;
    private final RequestLog log;

    /** @param platformContext an https URL created before this factory was installed. */
    ArumaStreamHandlerFactory(ArumaBase base, URL platformContext, RequestLog log) {
        this.base = base;
        this.platformContext = platformContext;
        this.log = log;
    }

    @Override
    public URLStreamHandler createURLStreamHandler(String protocol) {
        return "https".equalsIgnoreCase(protocol) ? new HttpsHandler() : null;
    }

    private final class HttpsHandler extends URLStreamHandler {
        @Override
        protected int getDefaultPort() {
            return 443;
        }

        @Override
        protected URLConnection openConnection(URL u) throws IOException {
            return open(u, null);
        }

        @Override
        protected URLConnection openConnection(URL u, Proxy proxy) throws IOException {
            return open(u, proxy);
        }

        private URLConnection open(URL u, Proxy proxy) throws IOException {
            String target = AuthEndpoints.rewrite(u, base);
            if (target != null && log != null) {
                log.redirected(u.getHost(), u.getPath());
            }
            URL actual = new URL(platformContext, target != null ? target : u.toExternalForm());
            return proxy == null ? actual.openConnection() : actual.openConnection(proxy);
        }
    }
}
