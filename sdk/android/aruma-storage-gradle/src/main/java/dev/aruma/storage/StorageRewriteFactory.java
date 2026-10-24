// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import com.android.build.api.instrumentation.AsmClassVisitorFactory;
import com.android.build.api.instrumentation.ClassContext;
import com.android.build.api.instrumentation.ClassData;
import com.android.build.api.instrumentation.InstrumentationParameters;
import org.gradle.api.provider.Property;
import org.gradle.api.tasks.Input;
import org.objectweb.asm.ClassVisitor;

/**
 * The instrumentation the plugin registers with {@code InstrumentationScope.ALL}: it sees the classes of every
 * dependency, and changes {@code NetworkRequest} alone. AGP runs it as a cached artifact transform on each
 * dependency's classes, so the factory cannot tell whether it ever ran; {@link VerifyArumaStorageTask} checks the
 * outcome on the final classes.
 */
public abstract class StorageRewriteFactory implements AsmClassVisitorFactory<StorageRewriteFactory.Parameters> {
    private static final long serialVersionUID = 1L;

    /** The instrumentation's inputs: a changed base invalidates the transformed classes. */
    public interface Parameters extends InstrumentationParameters {
        /** {@code <scheme>://<host>[:port]/v0}, from {@link StorageBase#of}. */
        @Input
        Property<String> getBase();
    }

    @Override
    public ClassVisitor createClassVisitor(ClassContext classContext, ClassVisitor nextClassVisitor) {
        return new NetworkRequestVisitor(nextClassVisitor, getParameters().get().getBase().get());
    }

    @Override
    public boolean isInstrumentable(ClassData classData) {
        return NetworkRequestVisitor.CLASS_NAME.equals(classData.getClassName());
    }
}
