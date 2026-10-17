#import <Foundation/Foundation.h>

NS_ASSUME_NONNULL_BEGIN

/// Runs block and returns the NSException it raised, or nil.
NSException *_Nullable ArumaCatchException(NS_NOESCAPE void (^block)(void));

NS_ASSUME_NONNULL_END
