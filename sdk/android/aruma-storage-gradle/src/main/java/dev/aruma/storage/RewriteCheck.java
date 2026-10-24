// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.io.UncheckedIOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Enumeration;
import java.util.List;
import java.util.stream.Stream;
import java.util.zip.ZipEntry;
import java.util.zip.ZipFile;
import org.objectweb.asm.ClassReader;
import org.objectweb.asm.ClassVisitor;
import org.objectweb.asm.MethodVisitor;
import org.objectweb.asm.Opcodes;

/**
 * The second layer of the plugin's closed failure: a check of the variant's final classes, the ones AGP dexes, after
 * every instrumentation. It does not trust that {@link NetworkRequestVisitor} ran: an {@code excludes} pattern, a
 * cached transform or another plugin's transform can each leave {@code NetworkRequest} as Google wrote it.
 *
 * <p>It reads the classes under {@code com/google/firebase/storage/} and nothing else. It passes when there is exactly
 * one {@code NetworkRequest}, it loads the Aruma base exactly once, in its static initializer, and never Google's, and
 * no other class there holds Google's host ({@link StorageBase#GOOGLE_HOST}) in any form: a second way to Google would
 * be one the plugin does not rewrite. NetworkRequest itself is judged by its {@code ldc}s, not its bytes: ASM keeps the
 * replaced text as an unused constant, which D8 drops. Classes outside firebase-storage are not its concern (aruma-redirect
 * itself names the host, to redirect it).
 */
final class RewriteCheck {
    private static final String PACKAGE = "com/google/firebase/storage/";
    private static final String CLASS_FILE = NetworkRequestVisitor.INTERNAL_NAME + ".class";
    private static final byte[] HOST_BYTES = StorageBase.GOOGLE_HOST.getBytes(StandardCharsets.UTF_8);

    private RewriteCheck() {}

    /**
     * Checks the classes in {@code jars} and {@code dirs} (class roots).
     *
     * @throws IllegalStateException with a message for the build log, when the check fails.
     */
    static void check(List<File> jars, List<File> dirs, String base) {
        List<String> copies = new ArrayList<>();
        List<String> leftovers = new ArrayList<>();
        byte[][] networkRequest = new byte[1][];
        ClassSink sink = (where, name, bytes) -> {
            if (name.equals(CLASS_FILE)) {
                copies.add(where);
                networkRequest[0] = bytes;
            } else if (indexOf(bytes, HOST_BYTES) >= 0) {
                leftovers.add(name + " in " + where);
            }
        };
        try {
            for (File jar : jars) {
                readJar(jar, sink);
            }
            for (File dir : dirs) {
                readDir(dir, sink);
            }
        } catch (IOException e) {
            throw new UncheckedIOException("dev.aruma.storage: cannot read the variant's classes", e);
        }

        if (copies.isEmpty()) {
            throw new IllegalStateException("dev.aruma.storage: " + NetworkRequestVisitor.CLASS_NAME
                    + " is not in this variant's classes, so firebase-storage is not in the app. Add "
                    + "com.google.firebase:firebase-storage, or remove the plugin.");
        }
        if (copies.size() > 1) {
            throw new IllegalStateException("dev.aruma.storage: " + copies.size() + " copies of "
                    + NetworkRequestVisitor.CLASS_NAME + " in this variant's classes: " + copies);
        }
        int[] counts = ldcCounts(networkRequest[0], base);
        if (counts[0] != 0 || counts[1] != 1) {
            throw new IllegalStateException("dev.aruma.storage: " + NetworkRequestVisitor.CLASS_NAME + " in "
                    + copies.get(0) + " was not pointed at Aruma (ldc of \"" + base + "\" in its static initializer: "
                    + counts[1] + ", ldc of \"" + StorageBase.GOOGLE + "\": " + counts[0] + "), so its requests would "
                    + "go to Google. The class was left out of the plugin's instrumentation (an excludes pattern on "
                    + "variant.instrumentation), or a later transform replaced it. Remove that, then build again.");
        }
        if (!leftovers.isEmpty()) {
            throw new IllegalStateException("dev.aruma.storage: classes of firebase-storage other than NetworkRequest hold \""
                    + StorageBase.GOOGLE_HOST + "\", which the plugin does not rewrite: " + leftovers
                    + ". This firebase-storage is not one the plugin knows.");
        }
    }

    /** {@code [ldc of Google's base anywhere, ldc of base in <clinit>]}. */
    static int[] ldcCounts(byte[] classBytes, String base) {
        int[] counts = new int[2];
        new ClassReader(classBytes).accept(new ClassVisitor(Opcodes.ASM9) {
            @Override
            public MethodVisitor visitMethod(int access, String name, String descriptor, String signature, String[] exceptions) {
                boolean clinit = name.equals("<clinit>");
                return new MethodVisitor(Opcodes.ASM9) {
                    @Override
                    public void visitLdcInsn(Object value) {
                        if (StorageBase.GOOGLE.equals(value)) {
                            counts[0]++;
                        } else if (clinit && base.equals(value)) {
                            counts[1]++;
                        }
                    }
                };
            }
        }, ClassReader.SKIP_DEBUG | ClassReader.SKIP_FRAMES);
        return counts;
    }

    private interface ClassSink {
        void accept(String where, String name, byte[] bytes);
    }

    private static void readJar(File jar, ClassSink sink) throws IOException {
        try (ZipFile zip = new ZipFile(jar)) {
            Enumeration<? extends ZipEntry> entries = zip.entries();
            while (entries.hasMoreElements()) {
                ZipEntry e = entries.nextElement();
                String name = e.getName();
                if (!e.isDirectory() && name.startsWith(PACKAGE) && name.endsWith(".class")) {
                    try (InputStream in = zip.getInputStream(e)) {
                        sink.accept(jar.getPath(), name, in.readAllBytes());
                    }
                }
            }
        }
    }

    private static void readDir(File dir, ClassSink sink) throws IOException {
        Path root = dir.toPath();
        Path pkg = root.resolve(PACKAGE);
        if (!Files.isDirectory(pkg)) {
            return;
        }
        try (Stream<Path> files = Files.walk(pkg)) {
            for (Path p : (Iterable<Path>) files::iterator) {
                if (Files.isRegularFile(p) && p.toString().endsWith(".class")) {
                    String name = root.relativize(p).toString().replace(File.separatorChar, '/');
                    sink.accept(dir.getPath(), name, Files.readAllBytes(p));
                }
            }
        }
    }

    static int indexOf(byte[] haystack, byte[] needle) {
        outer:
        for (int i = 0; i + needle.length <= haystack.length; i++) {
            for (int j = 0; j < needle.length; j++) {
                if (haystack[i + j] != needle[j]) {
                    continue outer;
                }
            }
            return i;
        }
        return -1;
    }
}
