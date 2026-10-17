#import "ExceptionCatcher.h"

NSException *ArumaCatchException(NS_NOESCAPE void (^block)(void)) {
  @try {
    block();
  } @catch (NSException *exception) {
    return exception;
  }
  return nil;
}
