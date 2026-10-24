// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Package adminstorage reaches Aruma's Cloud Storage from a Go server with the official
// cloud.google.com/go/storage client (ADR-022 §14): a *storage.Client of its own, separate from
// firebase.App, on option.WithEndpoint("<aruma>/storage/v1/") with storage.WithJSONReads(), since
// Aruma serves the JSON API and not XML, and the service account's credentials. It refuses
// STORAGE_EMULATOR_HOST, whose mode sends no credential, and builds download URLs in the one
// form every platform resolves: https://<aruma>/v0/b/<bucket>/o/<path>?alt=media&token=<token>.
//
// It is its own module, and imports nothing of the server. S4-D writes it.
package adminstorage
