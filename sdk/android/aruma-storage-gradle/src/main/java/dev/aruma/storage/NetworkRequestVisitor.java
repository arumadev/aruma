// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import org.objectweb.asm.ClassVisitor;
import org.objectweb.asm.MethodVisitor;
import org.objectweb.asm.Opcodes;

/**
 * Rewrites {@code com.google.firebase.storage.network.NetworkRequest}: the one {@code ldc} of
 * {@link StorageBase#GOOGLE} in its static initializer, which {@code Uri.parse} turns into {@code PROD_BASE_URL},
 * loads the Aruma base instead. {@code PROD_BASE_URL} is a {@code static final Uri} with no ConstantValue, set by
 * {@code putstatic} and read by {@code getstatic}, so no other class holds a copy (ADR-022 §16.2).
 *
 * <p>Nothing else is read or changed. The class must hold the text exactly once, in {@code <clinit>}: anything else is
 * a firebase-storage this plugin does not know, and the visitor throws at the end of the class, which fails the
 * build. A build that went on would send the app's requests, and its users' ID tokens, to Google.
 */
final class NetworkRequestVisitor extends ClassVisitor {
    static final String CLASS_NAME = "com.google.firebase.storage.network.NetworkRequest";
    static final String INTERNAL_NAME = "com/google/firebase/storage/network/NetworkRequest";

    private final String base;
    private int inClinit;
    private int elsewhere;

    NetworkRequestVisitor(ClassVisitor next, String base) {
        super(Opcodes.ASM9, next);
        this.base = base;
    }

    @Override
    public MethodVisitor visitMethod(int access, String name, String descriptor, String signature, String[] exceptions) {
        MethodVisitor mv = super.visitMethod(access, name, descriptor, signature, exceptions);
        boolean clinit = name.equals("<clinit>");
        return new MethodVisitor(Opcodes.ASM9, mv) {
            @Override
            public void visitLdcInsn(Object value) {
                if (StorageBase.GOOGLE.equals(value)) {
                    if (clinit) {
                        inClinit++;
                        super.visitLdcInsn(base);
                        return;
                    }
                    elsewhere++;
                }
                super.visitLdcInsn(value);
            }
        };
    }

    @Override
    public void visitEnd() {
        if (inClinit != 1 || elsewhere != 0) {
            throw new IllegalStateException(message(inClinit, elsewhere));
        }
        super.visitEnd();
    }

    static String message(int inClinit, int elsewhere) {
        return "dev.aruma.storage: " + CLASS_NAME + " does not load the text \"" + StorageBase.GOOGLE
                + "\" exactly once, in its static initializer (found " + inClinit + " there and " + elsewhere
                + " elsewhere). This firebase-storage is not one the plugin knows how to point at Aruma, and the build "
                + "stops here so that no request goes to Google. Use a firebase-storage version the plugin supports, "
                + "or a newer dev.aruma.storage.";
    }
}
