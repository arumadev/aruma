// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package dev.aruma.redirect;

/** Thrown when Aruma redirection is configured but cannot be put in place. Nothing is sent to Google then. */
public final class ArumaRedirectException extends RuntimeException {
    private static final long serialVersionUID = 1L;

    public ArumaRedirectException(String message) {
        super(message);
    }

    public ArumaRedirectException(String message, Throwable cause) {
        super(message, cause);
    }
}
