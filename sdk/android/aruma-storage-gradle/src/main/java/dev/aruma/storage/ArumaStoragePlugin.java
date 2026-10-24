// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import com.android.build.api.artifact.ScopedArtifact;
import com.android.build.api.instrumentation.InstrumentationScope;
import com.android.build.api.variant.ApplicationAndroidComponentsExtension;
import com.android.build.api.variant.ApplicationVariant;
import com.android.build.api.variant.ScopedArtifacts;
import com.android.build.api.variant.SourceDirectories;
import java.util.ArrayDeque;
import java.util.Deque;
import java.util.HashSet;
import java.util.Set;
import kotlin.Unit;
import org.gradle.api.Action;
import org.gradle.api.GradleException;
import org.gradle.api.Plugin;
import org.gradle.api.Project;
import org.gradle.api.artifacts.ModuleVersionIdentifier;
import org.gradle.api.artifacts.result.DependencyResult;
import org.gradle.api.artifacts.result.ResolvedComponentResult;
import org.gradle.api.artifacts.result.ResolvedDependencyResult;
import org.gradle.api.tasks.TaskProvider;

/**
 * {@code dev.aruma.storage} (ADR-022 §16.2): points the official firebase-storage at an Aruma server at build time.
 * In each variant of the app:
 *
 * <ol>
 *   <li>{@link StorageRewriteFactory} rewrites {@code NetworkRequest.PROD_BASE_URL}'s text over every dependency's
 *       classes, and throws on a {@code NetworkRequest} it does not know.
 *   <li>{@link VerifyArumaStorageTask} checks the final classes and writes the notice of the change into the app's
 *       Java resources: no APK or bundle without both.
 * </ol>
 *
 * The third layer is at run time, in aruma-redirect: an app whose {@code PROD_BASE_URL} is still Google's stops at
 * launch unless Storage is redirected there.
 */
public final class ArumaStoragePlugin implements Plugin<Project> {
    private static final String ANDROID_APP = "com.android.application";

    @Override
    public void apply(Project project) {
        ArumaStorageExtension ext = project.getExtensions().create("arumaStorage", ArumaStorageExtension.class);
        ext.getAllowInsecure().convention(false);
        project.getPluginManager().withPlugin(ANDROID_APP, p -> configure(project, ext));
        project.afterEvaluate(p -> {
            if (!p.getPluginManager().hasPlugin(ANDROID_APP)) {
                throw new GradleException("dev.aruma.storage: apply it to the Android application module, next to "
                        + ANDROID_APP + " (" + p.getPath() + " has no " + ANDROID_APP + ")");
            }
        });
    }

    private static void configure(Project project, ArumaStorageExtension ext) {
        ApplicationAndroidComponentsExtension components =
                project.getExtensions().getByType(ApplicationAndroidComponentsExtension.class);
        Action<ApplicationVariant> each = variant -> configure(project, ext, variant);
        components.onVariants(components.selector().all(), each);
    }

    private static void configure(Project project, ArumaStorageExtension ext, ApplicationVariant variant) {
        String base;
        try {
            base = StorageBase.of(ext.getUrl().getOrNull(), ext.getAllowInsecure().get(), variant.getDebuggable());
        } catch (IllegalArgumentException e) {
            throw new GradleException("dev.aruma.storage (" + project.getPath() + ", " + variant.getName() + "): "
                    + e.getMessage(), e);
        }

        variant.getInstrumentation().transformClassesWith(StorageRewriteFactory.class, InstrumentationScope.ALL, params -> {
            params.getBase().set(base);
            return Unit.INSTANCE;
        });

        TaskProvider<VerifyArumaStorageTask> verify = project.getTasks().register(
                variant.computeTaskName("verify", "ArumaStorage"), VerifyArumaStorageTask.class, t -> {
                    t.setGroup("verification");
                    t.setDescription("Checks that firebase-storage in " + variant.getName()
                            + " goes to Aruma, and writes the notice of that change.");
                    t.getBase().set(base);
                    t.getFirebaseStorageVersion().set(variant.getRuntimeConfiguration().getIncoming()
                            .getResolutionResult().getRootComponent().map(ArumaStoragePlugin::firebaseStorageVersion));
                    t.getOutputDirectory().set(project.getLayout().getBuildDirectory()
                            .dir("generated/aruma-storage/" + variant.getName()));
                });
        variant.getArtifacts().forScope(ScopedArtifacts.Scope.ALL).use(verify).toGet(ScopedArtifact.CLASSES.INSTANCE,
                VerifyArumaStorageTask::getAllJars, VerifyArumaStorageTask::getAllDirectories);

        SourceDirectories.Flat resources = variant.getSources().getResources();
        if (resources == null) {
            throw new GradleException("dev.aruma.storage (" + variant.getName()
                    + "): the variant has no Java resources, where the notice of the change to firebase-storage goes");
        }
        resources.addGeneratedSourceDirectory(verify, VerifyArumaStorageTask::getOutputDirectory);
    }

    /** The version of com.google.firebase:firebase-storage in a resolved runtime classpath, or "unknown". */
    static String firebaseStorageVersion(ResolvedComponentResult root) {
        Deque<ResolvedComponentResult> todo = new ArrayDeque<>();
        Set<ResolvedComponentResult> seen = new HashSet<>();
        todo.add(root);
        while (!todo.isEmpty()) {
            ResolvedComponentResult c = todo.poll();
            if (!seen.add(c)) {
                continue;
            }
            ModuleVersionIdentifier id = c.getModuleVersion();
            if (id != null && id.getGroup().equals("com.google.firebase") && id.getName().equals("firebase-storage")) {
                return id.getVersion();
            }
            for (DependencyResult d : c.getDependencies()) {
                if (d instanceof ResolvedDependencyResult r) {
                    todo.add(r.getSelected());
                }
            }
        }
        return "unknown";
    }
}
