// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import org.objectweb.asm.ClassReader;
import org.objectweb.asm.ClassWriter;
import org.objectweb.asm.MethodVisitor;
import org.objectweb.asm.Opcodes;

/**
 * Synthetic classes in the shape the plugin matches, built with ASM for the tests: a static initializer that loads
 * strings and stores them in a static field, and a second method that loads strings too. No Google class is read.
 */
final class Classes {
    private Classes() {}

    static byte[] networkRequest(String[] clinit, String[] elsewhere) {
        return make(NetworkRequestVisitor.INTERNAL_NAME, clinit, elsewhere);
    }

    static byte[] make(String internalName, String[] clinit, String[] elsewhere) {
        ClassWriter cw = new ClassWriter(ClassWriter.COMPUTE_MAXS | ClassWriter.COMPUTE_FRAMES);
        cw.visit(Opcodes.V1_8, Opcodes.ACC_PUBLIC | Opcodes.ACC_SUPER, internalName, null, "java/lang/Object", null);
        cw.visitField(Opcodes.ACC_PUBLIC | Opcodes.ACC_STATIC | Opcodes.ACC_FINAL, "PROD_BASE_URL", "Ljava/lang/String;", null, null)
                .visitEnd();
        MethodVisitor mv = cw.visitMethod(Opcodes.ACC_STATIC, "<clinit>", "()V", null, null);
        mv.visitCode();
        for (String s : clinit) {
            mv.visitLdcInsn(s);
            mv.visitFieldInsn(Opcodes.PUTSTATIC, internalName, "PROD_BASE_URL", "Ljava/lang/String;");
        }
        mv.visitInsn(Opcodes.RETURN);
        mv.visitMaxs(0, 0);
        mv.visitEnd();
        mv = cw.visitMethod(Opcodes.ACC_PUBLIC | Opcodes.ACC_STATIC, "other", "()V", null, null);
        mv.visitCode();
        for (String s : elsewhere) {
            mv.visitLdcInsn(s);
            mv.visitInsn(Opcodes.POP);
        }
        mv.visitInsn(Opcodes.RETURN);
        mv.visitMaxs(0, 0);
        mv.visitEnd();
        cw.visitEnd();
        return cw.toByteArray();
    }

    /** Runs the plugin's visitor over {@code in}, as AGP's instrumentation chains it. */
    static byte[] rewrite(byte[] in, String base) {
        ClassWriter cw = new ClassWriter(0);
        new ClassReader(in).accept(new NetworkRequestVisitor(cw, base), 0);
        return cw.toByteArray();
    }
}
