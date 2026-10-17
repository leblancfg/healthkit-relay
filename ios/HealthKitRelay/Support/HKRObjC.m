#import "HKRObjC.h"

NSString *_Nullable HKRTry(NS_NOESCAPE void (^block)(void)) {
    @try {
        block();
        return nil;
    } @catch (NSException *exception) {
        return [NSString stringWithFormat:@"%@: %@", exception.name, exception.reason ?: @""];
    }
}

id _Nullable HKRCallClassMethod(NSString *className, NSString *selector, id _Nullable argument) {
    Class cls = NSClassFromString(className);
    SEL sel = NSSelectorFromString(selector);
    if (cls == Nil || ![cls respondsToSelector:sel]) {
        return nil;
    }
    NSMethodSignature *signature = [cls methodSignatureForSelector:sel];
    NSUInteger args = signature.numberOfArguments - 2;
    if (strcmp(signature.methodReturnType, @encode(id)) != 0 || args != (argument ? 1u : 0u)) {
        return nil;
    }
    @try {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Warc-performSelector-leaks"
        return argument ? [cls performSelector:sel withObject:argument] : [cls performSelector:sel];
#pragma clang diagnostic pop
    } @catch (NSException *exception) {
        return nil;
    }
}

HKUnit *_Nullable HKRUnitFromString(NSString *string) {
    if (string.length == 0) {
        return nil;
    }
    @try {
        return [HKUnit unitFromString:string];
    } @catch (NSException *exception) {
        return nil;
    }
}
