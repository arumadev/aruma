// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Package adminstorage reaches Aruma's Cloud Storage from a Go server with the official
// cloud.google.com/go/storage client (ADR-022 §14): a *storage.Client of its own, separate from
// firebase.App, on option.WithEndpoint("<aruma>/storage/v1/") with storage.WithJSONReads(), since
// Aruma serves the JSON API and not XML, and the service account's credentials. It refuses
// STORAGE_EMULATOR_HOST, whose mode sends no credential, and builds download URLs in the one
// form every platform resolves: https://<aruma>/v0/b/<bucket>/o/<path>?alt=media&token=<token>.
//
// The one line a server changes:
//
//	client, err := app.Storage(ctx)            // before
//	client, err := adminstorage.NewClient(ctx) // after
//
// Client.Bucket and Client.DefaultBucket return the official *storage.BucketHandle, so the code
// after that line stays as it is. Do not pass option.WithEndpoint to firebase.NewApp instead:
// firebase-admin-go hands the app's options to every service, and Cloud Messaging then posts to
// <aruma>/storage/v1//projects/…/messages:send (docs/research/storage-production-path.md).
//
// It is its own module, and imports nothing of the server.
package adminstorage
