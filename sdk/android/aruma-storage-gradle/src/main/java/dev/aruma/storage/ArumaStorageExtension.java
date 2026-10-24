// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import org.gradle.api.provider.Property;

/**
 * {@code arumaStorage { }} in the app module's build script.
 *
 * <pre>
 * arumaStorage {
 *     url = "https://aruma.example.com"
 * }
 * </pre>
 */
public abstract class ArumaStorageExtension {
    /** The Aruma server, {@code https://<host>[:port]}: the same server as {@code ArumaURL}, without a path. */
    public abstract Property<String> getUrl();

    /** Accept http to any host. Development only; default false. */
    public abstract Property<Boolean> getAllowInsecure();
}
