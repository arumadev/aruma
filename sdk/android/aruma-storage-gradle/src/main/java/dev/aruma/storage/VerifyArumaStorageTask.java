// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.storage;

import java.io.File;
import java.io.IOException;
import java.io.UncheckedIOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.stream.Collectors;
import org.gradle.api.DefaultTask;
import org.gradle.api.GradleException;
import org.gradle.api.file.Directory;
import org.gradle.api.file.DirectoryProperty;
import org.gradle.api.file.RegularFile;
import org.gradle.api.provider.ListProperty;
import org.gradle.api.provider.Property;
import org.gradle.api.tasks.CacheableTask;
import org.gradle.api.tasks.Input;
import org.gradle.api.tasks.InputFiles;
import org.gradle.api.tasks.OutputDirectory;
import org.gradle.api.tasks.PathSensitive;
import org.gradle.api.tasks.PathSensitivity;
import org.gradle.api.tasks.TaskAction;

/**
 * Checks a variant's final classes ({@link RewriteCheck}) and, only when they pass, writes the notice that firebase-storage
 * was changed ({@link #NOTICE_PATH}) into a generated Java resources directory of the variant. The APK and the App
 * Bundle cannot be packaged without that directory, so neither can be built from classes that fail the check.
 */
@CacheableTask
public abstract class VerifyArumaStorageTask extends DefaultTask {
    /** A path of its own: AGP's default packaging excludes may drop the common META-INF/NOTICE names. */
    public static final String NOTICE_PATH = "META-INF/aruma/firebase-storage-MODIFIED.txt";

    /** The variant's final classes, jars (AGP: {@code ScopedArtifacts.Scope.ALL}, {@code CLASSES}). */
    @InputFiles
    @PathSensitive(PathSensitivity.NONE)
    public abstract ListProperty<RegularFile> getAllJars();

    /** The variant's final classes, directories. */
    @InputFiles
    @PathSensitive(PathSensitivity.RELATIVE)
    public abstract ListProperty<Directory> getAllDirectories();

    /** {@code <scheme>://<host>[:port]/v0}. */
    @Input
    public abstract Property<String> getBase();

    /** The resolved com.google.firebase:firebase-storage version, for the notice. */
    @Input
    public abstract Property<String> getFirebaseStorageVersion();

    @OutputDirectory
    public abstract DirectoryProperty getOutputDirectory();

    @TaskAction
    public void verify() {
        String base = getBase().get();
        try {
            RewriteCheck.check(files(getAllJars().get()), files(getAllDirectories().get()), base);
        } catch (IllegalStateException e) {
            throw new GradleException(e.getMessage(), e);
        }
        Path out = getOutputDirectory().get().getAsFile().toPath();
        Path notice = out.resolve(NOTICE_PATH);
        try {
            Files.createDirectories(notice.getParent());
            Files.writeString(notice, notice(getFirebaseStorageVersion().get(), base), StandardCharsets.UTF_8);
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
        getLogger().info("dev.aruma.storage: {} loads {} in {}", NetworkRequestVisitor.CLASS_NAME, base, getPath());
    }

    private static List<File> files(List<? extends org.gradle.api.file.FileSystemLocation> locations) {
        return locations.stream().map(org.gradle.api.file.FileSystemLocation::getAsFile).collect(Collectors.toList());
    }

    /** The notice Apache-2.0 §4(b) asks for: which file was changed, how, and by whom. */
    static String notice(String firebaseStorageVersion, String base) {
        String plugin = VerifyArumaStorageTask.class.getPackage().getImplementationVersion();
        return "This application contains a modified copy of firebase-storage for Android\n"
                + "(com.google.firebase:firebase-storage " + firebaseStorageVersion + "), Copyright Google LLC,\n"
                + "licensed under the Apache License, Version 2.0\n"
                + "(https://www.apache.org/licenses/LICENSE-2.0).\n"
                + "\n"
                + "Changed file: " + NetworkRequestVisitor.INTERNAL_NAME + ".class\n"
                + "Change: at build time, the Aruma Gradle plugin dev.aruma.storage"
                + (plugin == null ? "" : " " + plugin) + "\n"
                + "replaced the text \"" + StorageBase.GOOGLE + "\" in the class's static\n"
                + "initializer with \"" + base + "\", so that the library sends its requests to\n"
                + "that Aruma server and builds its download URLs on it. Nothing else was changed.\n"
                + "\n"
                + "Aruma: https://github.com/arumadev/aruma (Apache License, Version 2.0).\n"
                + "Aruma is not affiliated with or endorsed by Google. Firebase is a trademark of Google LLC.\n";
    }
}
