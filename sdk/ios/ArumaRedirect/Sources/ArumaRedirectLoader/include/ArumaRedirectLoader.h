// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

#import <Foundation/Foundation.h>

NS_ASSUME_NONNULL_BEGIN

/// Installs ArumaRedirect at launch from Info.plist (`ArumaURL`). Apps never call it:
/// the Objective-C runtime runs its `+load` when the binary is loaded, before `main`.
@interface ArumaRedirectLoader : NSObject
@end

NS_ASSUME_NONNULL_END
