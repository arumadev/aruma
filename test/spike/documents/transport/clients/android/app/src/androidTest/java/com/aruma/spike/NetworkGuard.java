package com.aruma.spike;

import java.io.IOException;
import java.net.Proxy;
import java.net.ProxySelector;
import java.net.SocketAddress;
import java.net.URI;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/**
 * The in-process half of the network guard: records every host this process connects to that is
 * not this device's loopback. The platform's HttpURLConnection asks the default ProxySelector for
 * each new connection, and grpc-okhttp asks it too (its ProxyDetector) for every channel it opens.
 *
 * <p>It only records. Blocking is the device half in run.sh: iptables rejects every packet of
 * this app's uid that does not stay on loopback, and the run fails if the counter moved.
 */
final class NetworkGuard extends ProxySelector {
    private static final List<String> outside = Collections.synchronizedList(new ArrayList<>());
    private final ProxySelector previous;

    private NetworkGuard(ProxySelector previous) {
        this.previous = previous;
    }

    static synchronized void install() {
        ProxySelector current = ProxySelector.getDefault();
        if (!(current instanceof NetworkGuard)) {
            ProxySelector.setDefault(new NetworkGuard(current));
        }
    }

    /** Connections opened to anything but loopback, as scheme://host (the platform asks per address). */
    static List<String> outside() {
        synchronized (outside) {
            return new ArrayList<>(outside);
        }
    }

    /** Drops the last n entries: the guard's own self-test. */
    static void forget(int n) {
        synchronized (outside) {
            for (int i = 0; i < n; i++) {
                outside.remove(outside.size() - 1);
            }
        }
    }

    @Override
    public List<Proxy> select(URI uri) {
        String host = uri.getHost();
        if (!"127.0.0.1".equals(host) && !"localhost".equals(host) && !"::1".equals(host) && !"[::1]".equals(host)) {
            outside.add(uri.getScheme() + "://" + host);
        }
        return previous != null ? previous.select(uri) : Collections.singletonList(Proxy.NO_PROXY);
    }

    @Override
    public void connectFailed(URI uri, SocketAddress address, IOException e) {
        if (previous != null) {
            previous.connectFailed(uri, address, e);
        }
    }
}
