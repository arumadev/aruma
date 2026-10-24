// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

import android.content.ContentProvider;
import android.content.ContentValues;
import android.content.pm.ApplicationInfo;
import android.content.pm.PackageManager;
import android.database.Cursor;
import android.net.Uri;
import android.os.Bundle;

/**
 * Reads {@code ArumaURL} from the app manifest and installs the redirect before {@code Application.onCreate}
 * and before FirebaseInitProvider (see {@code android:initOrder} in this library's manifest). A provider that throws
 * from {@code onCreate} stops the app at launch, which is the intent when ArumaURL is set but unusable, and when
 * Cloud Storage would reach Google ({@link StorageGuard}).
 */
public final class ArumaRedirectProvider extends ContentProvider {
    @Override
    public boolean onCreate() {
        ApplicationInfo info;
        try {
            info = getContext().getPackageManager()
                    .getApplicationInfo(getContext().getPackageName(), PackageManager.GET_META_DATA);
        } catch (PackageManager.NameNotFoundException e) {
            throw new ArumaRedirectException("ArumaRedirect cannot read the app manifest", e);
        }
        Bundle meta = info.metaData;
        if (meta != null) {
            boolean debuggable = (info.flags & ApplicationInfo.FLAG_DEBUGGABLE) != 0;
            ArumaRedirect.installFromValues(meta.get(ArumaRedirect.META_URL),
                    meta.get(ArumaRedirect.META_ALLOW_INSECURE), meta.get(ArumaRedirect.META_LOG),
                    meta.get(ArumaRedirect.META_STORAGE_REDIRECT), debuggable);
        }
        return true;
    }

    @Override
    public Cursor query(Uri uri, String[] projection, String selection, String[] selectionArgs, String sortOrder) {
        return null;
    }

    @Override
    public String getType(Uri uri) {
        return null;
    }

    @Override
    public Uri insert(Uri uri, ContentValues values) {
        return null;
    }

    @Override
    public int delete(Uri uri, String selection, String[] selectionArgs) {
        return 0;
    }

    @Override
    public int update(Uri uri, ContentValues values, String selection, String[] selectionArgs) {
        return 0;
    }
}
