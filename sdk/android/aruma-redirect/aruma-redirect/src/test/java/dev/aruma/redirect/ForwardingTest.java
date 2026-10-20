// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.net.URL;
import java.net.URLConnection;
import java.net.URLStreamHandler;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import javax.net.ssl.HttpsURLConnection;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

/** The handler against a real local HTTP server standing in for Aruma. */
public class ForwardingTest {
        private final List<String> seen = new ArrayList<>();
    private String lastBody;
    private String lastContentType;
    private String lastCustomHeader;
    private String lastMethod;
    private int status = 200;
    private String response = "{\"ok\":true}";
    private URLStreamHandler handler;

    private ServerSocket server;
    private int port() {
        return server.getLocalPort();
    }

    @Before
    public void setUp() throws Exception {
        server = new ServerSocket(0, 50, InetAddress.getByName("127.0.0.1"));
        Thread t = new Thread(() -> {
            while (!server.isClosed()) {
                try (Socket s = server.accept()) {
                    serve(s);
                } catch (IOException ignored) {
                    // closed, or a TLS probe on the plain port
                }
            }
        });
        t.setDaemon(true);
        t.start();
        ArumaBase base = new ArumaBase("http://127.0.0.1:" + port(), false, false);
        ArumaStreamHandlerFactory factory = new ArumaStreamHandlerFactory(base, new URL("https://aruma.invalid/"), null);
        handler = factory.createURLStreamHandler("https");
    }

    private void serve(Socket s) throws IOException {
        InputStream in = s.getInputStream();
        String requestLine = line(in);
        if (requestLine == null || requestLine.isEmpty() || !requestLine.contains("HTTP/")) {
            return;
        }
        String[] parts = requestLine.split(" ");
        int length = 0;
        String ct = null;
        String custom = null;
        for (String h; (h = line(in)) != null && !h.isEmpty(); ) {
            String lower = h.toLowerCase();
            if (lower.startsWith("content-length:")) {
                length = Integer.parseInt(h.substring(15).trim());
            } else if (lower.startsWith("content-type:")) {
                ct = h.substring(13).trim();
            } else if (lower.startsWith("x-client-version:")) {
                custom = h.substring(17).trim();
            }
        }
        byte[] body = new byte[length];
        for (int off = 0; off < length; ) {
            int n = in.read(body, off, length - off);
            if (n < 0) {
                break;
            }
            off += n;
        }
        synchronized (seen) {
            seen.add(parts[1]);
            lastMethod = parts[0];
            lastContentType = ct;
            lastCustomHeader = custom;
            lastBody = new String(body, StandardCharsets.UTF_8);
        }
        byte[] out = response.getBytes(StandardCharsets.UTF_8);
        String head = "HTTP/1.1 " + status + (status == 200 ? " OK" : " Bad Request") + "\r\n"
                + "Content-Type: application/json\r\nX-Reply: yes\r\nConnection: close\r\n"
                + "Content-Length: " + out.length + "\r\n\r\n";
        OutputStream os = s.getOutputStream();
        os.write(head.getBytes(StandardCharsets.US_ASCII));
        os.write(out);
        os.flush();
    }

    private static String line(InputStream in) throws IOException {
        StringBuilder sb = new StringBuilder();
        for (int c; (c = in.read()) >= 0; ) {
            if (c == '\n') {
                return sb.toString();
            }
            if (c != '\r') {
                sb.append((char) c);
            }
        }
        return sb.length() == 0 ? null : sb.toString();
    }

    @After
    public void tearDown() throws IOException {
        server.close();
    }

    private URL googleUrl(String spec) throws Exception {
        return new URL(null, spec, handler);
    }

    private static byte[] readAll(InputStream in) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buf = new byte[4096];
        for (int n; (n = in.read(buf)) > 0; ) {
            out.write(buf, 0, n);
        }
        return out.toByteArray();
    }

    @Test
    public void postKeepsMethodHeadersBodyAndQuery() throws Exception {
        URLConnection c = googleUrl("https://www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword?key=K").openConnection();
        assertTrue(c instanceof HttpURLConnection);
        HttpURLConnection h = (HttpURLConnection) c;
        h.setRequestMethod("POST");
        h.setRequestProperty("Content-Type", "application/json");
        h.setRequestProperty("X-Client-Version", "Android/Fallback/X22/FirebaseCore-Android");
        h.setDoOutput(true);
        byte[] body = "{\"email\":\"a@b.c\",\"password\":\"pw\"}".getBytes(StandardCharsets.UTF_8);
        h.setFixedLengthStreamingMode(body.length);
        try (OutputStream os = h.getOutputStream()) {
            os.write(body);
        }
        assertEquals(200, h.getResponseCode());
        assertEquals("yes", h.getHeaderField("X-Reply"));
        assertEquals("{\"ok\":true}", new String(readAll(h.getInputStream()), StandardCharsets.UTF_8));
        assertEquals("POST", lastMethod);
        assertEquals("application/json", lastContentType);
        assertEquals("Android/Fallback/X22/FirebaseCore-Android", lastCustomHeader);
        assertEquals("{\"email\":\"a@b.c\",\"password\":\"pw\"}", lastBody);
        assertEquals("/www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword?key=K", seen.get(0));
    }

    @Test
    public void getWorks() throws Exception {
        HttpURLConnection h = (HttpURLConnection) googleUrl("https://securetoken.googleapis.com/v1/token?key=K").openConnection();
        assertEquals(200, h.getResponseCode());
        assertEquals("GET", lastMethod);
        assertEquals("/securetoken.googleapis.com/v1/token?key=K", seen.get(0));
    }

    @Test
    public void errorStatusIsReportedWithAnErrorStreamAndInputStreamThrows() throws Exception {
        status = 400;
        response = "{\"error\":{\"message\":\"EMAIL_EXISTS\"}}";
        HttpURLConnection h = (HttpURLConnection) googleUrl(
                "https://www.googleapis.com/identitytoolkit/v3/relyingparty/signupNewUser?key=K").openConnection();
        h.setRequestMethod("POST");
        h.setDoOutput(true);
        try (OutputStream os = h.getOutputStream()) {
            os.write("{}".getBytes(StandardCharsets.UTF_8));
        }
        assertEquals(400, h.getResponseCode());
        assertEquals(response, new String(readAll(h.getErrorStream()), StandardCharsets.UTF_8));
        try {
            h.getInputStream();
            fail("getInputStream must throw for a 4xx");
        } catch (IOException expected) {
            // same as a normal HttpURLConnection
        }
    }

    @Test
    public void nonAuthHttpsGoesToThePlatformHandlerNotToAruma() throws Exception {
        URLConnection c = googleUrl("https://firestore.googleapis.com/v1/projects/p").openConnection();
        assertTrue("platform https connection expected", c instanceof HttpsURLConnection);
        URLConnection local = googleUrl("https://127.0.0.1:" + port() + "/x").openConnection();
        assertTrue(local instanceof HttpsURLConnection);
        try {
            ((HttpsURLConnection) local).getResponseCode(); // plain HTTP server: TLS handshake fails
            fail();
        } catch (IOException expected) {
            // fine: it was not rewritten
        }
        assertEquals("nothing may reach Aruma", 0, seen.size());
    }

    @Test
    public void refusedAuthShapeThrowsFromOpenConnection() throws Exception {
        try {
            googleUrl("https://securetoken.googleapis.com:8443/v1/token").openConnection();
            fail();
        } catch (IOException expected) {
            assertTrue(expected.getMessage().contains("refused"));
        }
        assertEquals(0, seen.size());
    }

    @Test
    public void handlerIsOnlyForHttps() throws Exception {
        ArumaBase base = new ArumaBase("https://aruma.example.com", false, false);
        ArumaStreamHandlerFactory f = new ArumaStreamHandlerFactory(base, new URL("https://aruma.invalid/"), null);
        assertNull(f.createURLStreamHandler("http"));
        assertNull(f.createURLStreamHandler("jar"));
        assertFalse(f.createURLStreamHandler("HTTPS") == null);
    }

    @Test
    public void requestLogReceivesHostAndPathOnly() throws Exception {
        final List<String> logged = new ArrayList<>();
        ArumaBase base = new ArumaBase("http://127.0.0.1:" + port(), false, false);
        URLStreamHandler h = new ArumaStreamHandlerFactory(base, new URL("https://aruma.invalid/"),
                (host, path) -> logged.add(host + path)).createURLStreamHandler("https");
        HttpURLConnection c = (HttpURLConnection) new URL(null, "https://securetoken.googleapis.com/v1/token?key=SECRET", h)
                .openConnection();
        assertEquals(200, c.getResponseCode());
        assertEquals(1, logged.size());
        assertEquals("securetoken.googleapis.com/v1/token", logged.get(0));
    }
}
