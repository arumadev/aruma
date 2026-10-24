package com.aruma.e2e;

import static org.junit.Assert.assertNotNull;

import androidx.test.ext.junit.runners.AndroidJUnit4;

import dev.aruma.redirect.ArumaRedirect;

import org.junit.Test;
import org.junit.runner.RunWith;

/**
 * Only that the app's process started, with aruma-redirect installed. run.sh runs it against the
 * build without dev.aruma.storage and with ArumaStorageRedirect=false, where aruma-redirect's
 * launch check must stop the process before this test, or Firebase, can run.
 */
@RunWith(AndroidJUnit4.class)
public class LaunchE2ETest {
    @Test
    public void processStarted() {
        assertNotNull(ArumaRedirect.baseUrl());
    }
}
