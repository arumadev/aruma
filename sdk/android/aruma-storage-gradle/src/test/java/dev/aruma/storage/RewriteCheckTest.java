// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Collections;
import java.util.List;
import java.util.zip.ZipEntry;
import java.util.zip.ZipOutputStream;
import org.junit.Rule;
import org.junit.Test;
import org.junit.rules.TemporaryFolder;

public class RewriteCheckTest {
    private static final String BASE = "https://aruma.example.com/v0";
    private static final String NR = NetworkRequestVisitor.INTERNAL_NAME + ".class";

    @Rule
    public final TemporaryFolder tmp = new TemporaryFolder();

    private static byte[] original() {
        return Classes.networkRequest(new String[] {StorageBase.GOOGLE}, new String[0]);
    }

    private static byte[] rewritten() {
        return Classes.rewrite(original(), BASE);
    }

    private File jar(String name, Object... entries) throws IOException {
        File f = tmp.newFile(name);
        try (ZipOutputStream z = new ZipOutputStream(new FileOutputStream(f))) {
            for (int i = 0; i < entries.length; i += 2) {
                z.putNextEntry(new ZipEntry((String) entries[i]));
                z.write((byte[]) entries[i + 1]);
                z.closeEntry();
            }
        }
        return f;
    }

    private File dir(String name, String entry, byte[] bytes) throws IOException {
        File root = tmp.newFolder(name);
        Path p = root.toPath().resolve(entry);
        Files.createDirectories(p.getParent());
        Files.write(p, bytes);
        return root;
    }

    @Test
    public void rewrittenClassesPass() throws IOException {
        File app = dir("app", "com/aruma/App.class", Classes.make("com/aruma/App", new String[] {"x"}, new String[0]));
        RewriteCheck.check(List.of(jar("storage.jar", NR, rewritten())), List.of(app), BASE);
        RewriteCheck.check(Collections.emptyList(), List.of(dir("classes", NR, rewritten())), BASE);
    }

    @Test
    public void anUnrewrittenClassFails() throws IOException {
        // What the final classes hold when NetworkRequest is excluded from the instrumentation.
        fails(List.of(jar("storage.jar", NR, original())), List.of(), "was not pointed at Aruma", "excludes");
    }

    @Test
    public void aClassRewrittenForAnotherServerFails() throws IOException {
        byte[] other = Classes.rewrite(original(), "https://old.example.com/v0");
        fails(List.of(jar("storage.jar", NR, other)), List.of(), "was not pointed at Aruma");
    }

    @Test
    public void noFirebaseStorageFails() throws IOException {
        fails(List.of(jar("other.jar", "com/example/A.class", new byte[] {1, 2})), List.of(), "firebase-storage is not in the app");
    }

    @Test
    public void twoCopiesFail() throws IOException {
        fails(List.of(jar("a.jar", NR, rewritten())), List.of(dir("classes", NR, rewritten())), "2 copies");
    }

    @Test
    public void theTextLeftInAnotherStorageClassFails() throws IOException {
        byte[] util = Classes.make("com/google/firebase/storage/internal/Util", new String[] {StorageBase.GOOGLE}, new String[0]);
        fails(List.of(jar("storage.jar", NR, rewritten(), "com/google/firebase/storage/internal/Util.class", util)), List.of(),
                "other than NetworkRequest");
    }

    @Test
    public void theTextOutsideFirebaseStorageIsNotItsConcern() throws IOException {
        byte[] other = Classes.make("com/example/Links", new String[] {StorageBase.GOOGLE}, new String[0]);
        RewriteCheck.check(List.of(jar("storage.jar", NR, rewritten(), "com/example/Links.class", other)), List.of(), BASE);
    }

    @Test
    public void theNoticeNamesTheFileTheChangeAndTheLicense() {
        String n = VerifyArumaStorageTask.notice("23.0.0", BASE);
        assertTrue(n, n.contains("com.google.firebase:firebase-storage 23.0.0"));
        assertTrue(n, n.contains("Apache License, Version 2.0"));
        assertTrue(n, n.contains("com/google/firebase/storage/network/NetworkRequest.class"));
        assertTrue(n, n.contains("\"" + StorageBase.GOOGLE + "\""));
        assertTrue(n, n.contains("\"" + BASE + "\""));
    }

    private static void fails(List<File> jars, List<File> dirs, String... details) {
        try {
            RewriteCheck.check(jars, dirs, BASE);
            fail("expected the check to fail");
        } catch (IllegalStateException e) {
            for (String d : details) {
                assertTrue(e.getMessage(), e.getMessage().contains(d));
            }
        }
    }
}
