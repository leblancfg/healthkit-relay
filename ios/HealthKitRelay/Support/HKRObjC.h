// Small Objective-C helpers for what Swift cannot do: catch the NSExceptions HealthKit raises
// for types an app may not request (instead of returning an error), and build objects from
// selector names found in the SDK headers at build time.

#import <Foundation/Foundation.h>
#import <HealthKit/HealthKit.h>

NS_ASSUME_NONNULL_BEGIN

/// Runs block; returns the reason of an Objective-C exception it raised, or nil.
NSString *_Nullable HKRTry(NS_NOESCAPE void (^block)(void));

/// [className selector] or [className selector:argument], if the class exists and responds to
/// it; nil otherwise or if it raised.
id _Nullable HKRCallClassMethod(NSString *className, NSString *selector, id _Nullable argument);

/// [HKUnit unitFromString:], nil instead of an exception for strings HealthKit cannot parse.
HKUnit *_Nullable HKRUnitFromString(NSString *string);

NS_ASSUME_NONNULL_END
