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
 * it for an https spec. With an http context it handles {@code http} the same way, only to refuse Cloud Storage on
 * plain http while the Storage redirect is off. Other protocols return null from the factory and keep the platform
 * defaults. A refused Storage request is answered with a {@link RefusedConnection}, never sent.
 */
final class ArumaStreamHandlerFactory implements URLStreamHandlerFactory {
    /** Receives host and path of each redirected request (never query, headers or body). May be null. */
    interface RequestLog {
        void redirected(String originalHost, String path);
    }

    private final ArumaBase base;
    private final URL platformContext;
    private final URL httpContext;
    private final RequestLog log;

    /** @param platformContext an https URL created before this factory was installed. It handles https alone. */
    ArumaStreamHandlerFactory(ArumaBase base, URL platformContext, RequestLog log) {
        this(base, platformContext, null, log);
    }

    /**
     * @param platformContext an https URL created before this factory was installed.
     * @param httpContext an http URL created before this factory was installed, or null to leave http alone.
     */
    ArumaStreamHandlerFactory(ArumaBase base, URL platformContext, URL httpContext, RequestLog log) {
        this.base = base;
        this.platformContext = platformContext;
        this.httpContext = httpContext;
        this.log = log;
    }

    @Override
    public URLStreamHandler createURLStreamHandler(String protocol) {
        if ("https".equalsIgnoreCase(protocol)) {
            return new Handler(platformContext, 443);
        }
        if ("http".equalsIgnoreCase(protocol) && httpContext != null) {
            return new Handler(httpContext, 80);
        }
        return null;
    }

    private final class Handler extends URLStreamHandler {
        private final URL context;
        private final int defaultPort;

        Handler(URL context, int defaultPort) {
            this.context = context;
            this.defaultPort = defaultPort;
        }

        @Override
        protected int getDefaultPort() {
            return defaultPort;
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
            String target;
            try {
                target = AuthEndpoints.rewrite(u, base);
            } catch (AuthEndpoints.StorageRefusedException e) {
                return new RefusedConnection(u, e.getMessage());
            }
            if (target != null && log != null) {
                log.redirected(u.getHost(), u.getPath());
            }
            URL actual = new URL(context, target != null ? target : u.toExternalForm());
            return proxy == null ? actual.openConnection() : actual.openConnection(proxy);
        }
    }
}
