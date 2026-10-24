// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.security.cert.Certificate;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.TreeMap;
import javax.net.ssl.HttpsURLConnection;
import javax.net.ssl.SSLPeerUnverifiedException;

/**
 * The answer to a Cloud Storage request ArumaRedirect refuses: a 403 made here, with nothing sent. A refusal thrown as
 * an {@code IOException} would be retried: firebase-storage's NetworkRequest turns it into NETWORK_UNAVAILABLE (-2),
 * which ExponentialBackoffSender.isRetryableError retries, with 5xx, 408 and 429, for up to 2 minutes (10 for
 * uploads). A 403 is not retried, and becomes {@code StorageException.ERROR_NOT_AUTHORIZED} at once, with the reason in
 * the body. The request's headers (the ID token) and body are kept by this object and never leave the process.
 */
final class RefusedConnection extends HttpsURLConnection {
    static final int STATUS = 403;

    private final String reason;
    private final byte[] body;

    RefusedConnection(URL url, String reason) {
        super(url);
        this.reason = reason;
        String message = reason.replace("\\", "\\\\").replace("\"", "\\\"");
        this.body = ("{\"error\":{\"code\":" + STATUS + ",\"message\":\"" + message + "\"}}").getBytes(StandardCharsets.UTF_8);
    }

    @Override
    public void connect() {
        connected = true;
    }

    @Override
    public void disconnect() {}

    @Override
    public boolean usingProxy() {
        return false;
    }

    @Override
    public int getResponseCode() {
        connected = true;
        return STATUS;
    }

    @Override
    public String getResponseMessage() {
        return "Forbidden";
    }

    @Override
    public InputStream getInputStream() throws IOException {
        throw new IOException(reason);
    }

    @Override
    public InputStream getErrorStream() {
        return new ByteArrayInputStream(body);
    }

    /** Takes the request body and drops it. */
    @Override
    public OutputStream getOutputStream() {
        return new OutputStream() {
            @Override
            public void write(int b) {}

            @Override
            public void write(byte[] b, int off, int len) {}
        };
    }

    @Override
    public String getHeaderField(String name) {
        if (name == null) {
            return null;
        }
        switch (name.toLowerCase(Locale.ROOT)) {
            case "content-type":
                return "application/json; charset=UTF-8";
            case "content-length":
                return Integer.toString(body.length);
            default:
                return null;
        }
    }

    @Override
    public Map<String, List<String>> getHeaderFields() {
        Map<String, List<String>> h = new TreeMap<>(String.CASE_INSENSITIVE_ORDER);
        h.put("Content-Type", Collections.singletonList(getHeaderField("content-type")));
        h.put("Content-Length", Collections.singletonList(getHeaderField("content-length")));
        return Collections.unmodifiableMap(h);
    }

    @Override
    public String getCipherSuite() {
        return null;
    }

    @Override
    public Certificate[] getLocalCertificates() {
        return null;
    }

    @Override
    public Certificate[] getServerCertificates() throws SSLPeerUnverifiedException {
        throw new SSLPeerUnverifiedException("refused by ArumaRedirect: no connection was made");
    }
}
