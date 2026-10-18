// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

#import "ArumaRedirectLoader.h"

@implementation ArumaRedirectLoader

// Runs once when the image is loaded, before main() and before Firebase is configured.
// Swift has no +load, so this small Objective-C target calls into the Swift module by name
// (an Objective-C target cannot import a Swift module of the same package directly).
+ (void)load {
  Class installer = NSClassFromString(@"ArumaRedirectAutoInstaller");
  SEL selector = NSSelectorFromString(@"installFromMainBundle");
  if (installer == Nil || ![installer respondsToSelector:selector]) {
    NSLog(@"ARUMA_REDIRECT loader: the Swift module is not linked; nothing installed");
    return;
  }
  ((void (*)(id, SEL))[installer methodForSelector:selector])(installer, selector);
}

@end
