//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <CoreServices/CoreServices.h>
#import <dispatch/dispatch.h>
#include "native_darwin.h"
#include <stdlib.h>
#include <string.h>

// The target is fixed to com.apple.mail. Cache only successful preflights for
// each exact event class/ID; AESendMessage still enforces TCC for every event.
static NSMutableSet<NSNumber *> *PermissionCache(void) {
    static NSMutableSet<NSNumber *> *cache;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ cache = [NSMutableSet set]; });
    return cache;
}

static NSNumber *PermissionKey(AEEventClass eventClass, AEEventID eventID) {
    return @(((uint64_t)eventClass << 32) | (uint64_t)eventID);
}

static OSStatus Preflight(NSAppleEventDescriptor *target, AEEventClass eventClass, AEEventID eventID, BOOL *cached) {
    NSMutableSet *cache = PermissionCache();
    NSNumber *key = PermissionKey(eventClass, eventID);
    @synchronized(cache) {
        if ([cache containsObject:key]) { *cached = YES; return noErr; }
        OSStatus status = AEDeterminePermissionToAutomateTarget(target.aeDesc, eventClass, eventID, false);
        if (status == noErr) [cache addObject:key];
        return status;
    }
}

static void InvalidatePermission(AEEventClass eventClass, AEEventID eventID) {
    NSMutableSet *cache = PermissionCache();
    @synchronized(cache) { [cache removeObject:PermissionKey(eventClass, eventID)]; }
}

// Internal diagnostic reset strengthens the next call by forcing a preflight.
void mail_ae_clear_permission_cache(void) {
    @autoreleasepool {
        NSMutableSet *cache = PermissionCache();
        @synchronized(cache) { [cache removeAllObjects]; }
    }
}

static void Fail(OSStatus code, NSString *message) {
    @throw [NSException exceptionWithName:@"MailAppleEventError" reason:message
        userInfo:@{@"code": @(code)}];
}

static OSType Code(NSString *text) {
    NSData *bytes = [text dataUsingEncoding:NSASCIIStringEncoding];
    if (bytes.length != 4) Fail(paramErr, @"Apple Event codes require four ASCII bytes");
    const unsigned char *p = bytes.bytes;
    OSType value = 0;
    for (int i = 0; i < 4; i++) {
        if (p[i] < 32 || p[i] > 126) Fail(paramErr, @"Apple Event codes require printable ASCII bytes");
        value = (value << 8) | p[i];
    }
    return value;
}

static NSString *CodeString(OSType code) {
    unsigned char bytes[4] = {code >> 24, code >> 16, code >> 8, code};
    for (int i = 0; i < 4; i++) if (bytes[i] < 32 || bytes[i] > 126) return nil;
    return [[NSString alloc] initWithBytes:bytes length:4 encoding:NSASCIIStringEncoding];
}

static NSAppleEventDescriptor *Coerce(NSAppleEventDescriptor *desc, DescType type) {
    if (!desc) return nil;
    AEDesc result = {typeNull, NULL};
    OSStatus status = AECoerceDesc(desc.aeDesc, type, &result);
    if (status != noErr) { AEDisposeDesc(&result); return nil; }
    return [[NSAppleEventDescriptor alloc] initWithAEDescNoCopy:&result];
}

static NSString *Flatten(NSAppleEventDescriptor *desc) {
    Size length = AESizeOfFlattenedDesc(desc.aeDesc);
    if (length <= 0) Fail(errAECoercionFail, @"Cannot flatten Apple Event descriptor");
    NSMutableData *data = [NSMutableData dataWithLength:(NSUInteger)length];
    Size actual = 0;
    OSStatus status = AEFlattenDesc(desc.aeDesc, data.mutableBytes, length, &actual);
    if (status != noErr) Fail(status, @"Cannot flatten Apple Event descriptor");
    data.length = (NSUInteger)actual;
    return [data base64EncodedStringWithOptions:0];
}

static NSAppleEventDescriptor *Unflatten(NSString *encoded) {
    NSData *data = [[NSData alloc] initWithBase64EncodedString:encoded options:0];
    if (!data || data.length == 0) Fail(paramErr, @"Invalid flattened Apple Event descriptor");
    AEDesc result = {typeNull, NULL};
    OSStatus status;
    if (@available(macOS 11.0, *)) {
        status = AEUnflattenDescFromBytes(data.bytes, data.length, &result);
    } else {
        Fail(unimpErr, @"Opaque Apple Event descriptors require macOS 11 or later");
        return nil;
    }
    if (status != noErr) { AEDisposeDesc(&result); Fail(status, @"Invalid flattened Apple Event descriptor"); }
    return [[NSAppleEventDescriptor alloc] initWithAEDescNoCopy:&result];
}

// Public legacy file-reference APIs are used solely to construct a requested
// alias/FSRef descriptor. File URLs remain the default portable representation.
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
static NSAppleEventDescriptor *FileReference(NSString *path, DescType type) {
    FSRef reference;
    OSStatus status = FSPathMakeRef((const UInt8 *)path.fileSystemRepresentation, &reference, NULL);
    if (status != noErr) Fail(status, @"Cannot resolve native file reference path");
    if (type == typeFSRef) return [NSAppleEventDescriptor descriptorWithDescriptorType:typeFSRef bytes:&reference length:sizeof(reference)];
    AliasHandle alias = NULL;
    status = FSNewAliasMinimal(&reference, &alias);
    if (status != noErr) { if (alias) DisposeHandle((Handle)alias); Fail(status, @"Cannot construct native alias descriptor"); }
    NSAppleEventDescriptor *result;
    @try {
        HLock((Handle)alias);
        result = [NSAppleEventDescriptor descriptorWithDescriptorType:typeAlias bytes:*alias length:(NSUInteger)GetHandleSize((Handle)alias)];
    } @finally {
        DisposeHandle((Handle)alias);
    }
    return result;
}
#pragma clang diagnostic pop

static NSAppleEventDescriptor *Encode(NSDictionary *value, int depth) {
    if (depth > 128) Fail(paramErr, @"Apple Event descriptor nesting exceeds 128");
    NSString *kind = value[@"kind"];
    // Text and file wrappers retain their original flattened descriptor
    // alongside the decoded value, preserving their native type on reuse.
    if ([kind isEqual:@"opaque"] || (([kind isEqual:@"string"] || [kind isEqual:@"file"]) && value[@"data"])) return Unflatten(value[@"data"]);
    if ([kind isEqual:@"null"]) return NSAppleEventDescriptor.nullDescriptor;
    if ([kind isEqual:@"examined"]) return [NSAppleEventDescriptor descriptorWithDescriptorType:typeObjectBeingExamined bytes:NULL length:0];
    if ([kind isEqual:@"current-container"]) return [NSAppleEventDescriptor descriptorWithDescriptorType:typeCurrentContainer bytes:NULL length:0];
    if ([kind isEqual:@"string"]) return [NSAppleEventDescriptor descriptorWithString:value[@"text"] ?: @""];
    if ([kind isEqual:@"int"]) {
        SInt64 number = [value[@"number"] longLongValue];
        return [NSAppleEventDescriptor descriptorWithDescriptorType:typeSInt64 bytes:&number length:sizeof(number)];
    }
    if ([kind isEqual:@"real"]) return [NSAppleEventDescriptor descriptorWithDouble:[value[@"real"] doubleValue]];
    if ([kind isEqual:@"bool"]) return [NSAppleEventDescriptor descriptorWithBoolean:[value[@"boolean"] boolValue]];
    if ([kind isEqual:@"ordinal"]) {
        OSType code = Code(value[@"code"]);
        return [NSAppleEventDescriptor descriptorWithDescriptorType:typeAbsoluteOrdinal bytes:&code length:sizeof(code)];
    }
    if ([kind isEqual:@"enum"]) return [NSAppleEventDescriptor descriptorWithEnumCode:Code(value[@"code"])];
    if ([kind isEqual:@"type"]) return [NSAppleEventDescriptor descriptorWithTypeCode:Code(value[@"code"])];
    if ([kind isEqual:@"file"]) {
        NSMutableCharacterSet *allowed = [NSCharacterSet.URLPathAllowedCharacterSet mutableCopy];
        [allowed removeCharactersInString:@"?#"];
        NSString *path = [value[@"text"] stringByAddingPercentEncodingWithAllowedCharacters:allowed];
        NSData *url = [[@"file://" stringByAppendingString:path] dataUsingEncoding:NSUTF8StringEncoding];
        NSAppleEventDescriptor *file = [NSAppleEventDescriptor descriptorWithDescriptorType:typeFileURL data:url];
        if ([value[@"code"] isEqual:@"alis"] || [value[@"code"] isEqual:@"fsrf"]) {
            file = FileReference(value[@"text"], Code(value[@"code"]));
            if (!file) Fail(errAECoercionFail, @"Cannot create native file reference descriptor");
        }
        return file;
    }
    if ([kind isEqual:@"date"]) {
        NSISO8601DateFormatter *formatter = [[NSISO8601DateFormatter alloc] init];
        formatter.formatOptions = NSISO8601DateFormatWithInternetDateTime | NSISO8601DateFormatWithFractionalSeconds;
        NSDate *date = [formatter dateFromString:value[@"text"]];
        if (!date) {
            formatter.formatOptions = NSISO8601DateFormatWithInternetDateTime;
            date = [formatter dateFromString:value[@"text"]];
        }
        if (!date) Fail(paramErr, @"Invalid RFC3339 Apple Event date");
        return [NSAppleEventDescriptor descriptorWithDate:date];
    }
    if ([kind isEqual:@"list"]) {
        NSAppleEventDescriptor *desc = NSAppleEventDescriptor.listDescriptor;
        for (NSDictionary *item in value[@"items"]) [desc insertDescriptor:Encode(item, depth+1) atIndex:0];
        return desc;
    }
    if ([kind isEqual:@"record"] || [kind isEqual:@"object"] || [kind isEqual:@"insertion"] || [kind isEqual:@"comparison"] || [kind isEqual:@"logical"] || [kind isEqual:@"range"]) {
        NSAppleEventDescriptor *desc = NSAppleEventDescriptor.recordDescriptor;
        NSDictionary *fields = value[@"fields"];
        for (NSString *key in fields) [desc setDescriptor:Encode(fields[key], depth+1) forKeyword:Code(key)];
        if ([kind isEqual:@"object"]) {
            desc = Coerce(desc, typeObjectSpecifier);
            if (!desc) Fail(errAECoercionFail, @"Cannot create Apple Event object specifier");
        }
        if ([kind isEqual:@"insertion"]) {
            desc = Coerce(desc, typeInsertionLoc);
            if (!desc) Fail(errAECoercionFail, @"Cannot create Apple Event insertion location");
        }
        if ([kind isEqual:@"comparison"]) {
            desc = Coerce(desc, typeCompDescriptor);
            if (!desc) Fail(errAECoercionFail, @"Cannot create Apple Event comparison descriptor");
        }
        if ([kind isEqual:@"logical"]) {
            desc = Coerce(desc, typeLogicalDescriptor);
            if (!desc) Fail(errAECoercionFail, @"Cannot create Apple Event logical descriptor");
        }
        if ([kind isEqual:@"range"]) {
            desc = Coerce(desc,typeRangeDescriptor);
            if (!desc) Fail(errAECoercionFail,@"Cannot create Apple Event range descriptor");
        }
        return desc;
    }
    Fail(paramErr, @"Unsupported Apple Event descriptor kind");
    return nil;
}

static NSDictionary *Opaque(NSAppleEventDescriptor *desc) {
    NSMutableDictionary *value = [@{@"kind": @"opaque", @"data": Flatten(desc)} mutableCopy];
    NSString *code = CodeString(desc.descriptorType);
    if (code) value[@"code"] = code;
    return value;
}

// NSISO8601DateFormatter setup is expensive for Mail's date columns. Keep
// the existing UTC/default-format behavior and serialize shared formatter use.
static NSString *UTCDateString(NSDate *date) {
    static NSISO8601DateFormatter *formatter;
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        formatter = [[NSISO8601DateFormatter alloc] init];
        formatter.timeZone = [NSTimeZone timeZoneForSecondsFromGMT:0];
    });
    @synchronized(formatter) { return [formatter stringFromDate:date]; }
}

static NSDictionary *Decode(NSAppleEventDescriptor *desc, int depth) {
    if (!desc) return @{@"kind": @"null"};
    if (depth > 128) Fail(paramErr, @"Apple Event reply nesting exceeds 128");
    DescType type = desc.descriptorType;
    if (type == typeNull) return @{@"kind": @"null"};
    if (type == typeObjectSpecifier) return Opaque(desc);
    if (type == typeBoolean || type == typeTrue || type == typeFalse) return @{@"kind": @"bool", @"boolean": @((BOOL)desc.booleanValue)};
    if (type == typeSInt16 || type == typeSInt32 || type == typeSInt64 || type == typeUInt32) {
        NSAppleEventDescriptor *number = Coerce(desc, typeSInt64);
        if (!number || number.data.length != sizeof(SInt64)) return Opaque(desc);
        SInt64 integer = 0;
        [number.data getBytes:&integer length:sizeof(integer)];
        return @{@"kind": @"int", @"number": @(integer)};
    }
    if (type == typeIEEE32BitFloatingPoint || type == typeIEEE64BitFloatingPoint) {
        double number = desc.doubleValue;
        if (!isfinite(number)) return Opaque(desc);
        return @{@"kind": @"real", @"real": @(number)};
    }
    if (type == typeEnumerated || type == typeType) {
        NSString *code = CodeString(type == typeEnumerated ? desc.enumCodeValue : desc.typeCodeValue);
        if (!code) return Opaque(desc);
        return @{@"kind": type == typeEnumerated ? @"enum" : @"type", @"code": code};
    }
    if (type == typeLongDateTime) {
        NSDate *date = desc.dateValue;
        if (!date) return Opaque(desc);
        return @{@"kind": @"date", @"text": UTCDateString(date)};
    }
    if (type == typeFileURL || type == typeAlias || type == typeFSRef) {
        NSAppleEventDescriptor *file = type == typeFileURL ? desc : Coerce(desc, typeFileURL);
        if (!file) return Opaque(desc);
        NSString *encodedURL = [[NSString alloc] initWithData:file.data encoding:NSUTF8StringEncoding];
        NSURL *url = encodedURL ? [NSURL URLWithString:encodedURL] : nil;
        if (!url.isFileURL || !url.path) return Opaque(desc);
        if (type != typeFileURL) return @{@"kind": @"file", @"text": url.path, @"code": CodeString(type), @"data": Flatten(desc)};
        return @{@"kind": @"file", @"text": url.path};
    }
    BOOL plainText = type == typeUnicodeText || type == typeUTF8Text || type == typeChar || type == typeCString;
    BOOL wrappedText = type == typeStyledText || type == typeAEText || type == 'ctxt' || type == 'RTF ';
    if (plainText || wrappedText) {
        // Use Apple's text coercion rather than interpreting legacy encodings
        // or rich-text bytes as UTF-8. Preserve wrappers for lossless reuse.
        NSAppleEventDescriptor *text = Coerce(desc, typeUnicodeText);
        NSString *string = text.stringValue;
        if (!string && wrappedText && desc.isRecordDescriptor) {
            NSAppleEventDescriptor *inner = [desc descriptorForKeyword:keyAEText];
            string = Coerce(inner, typeUnicodeText).stringValue;
        }
        if (!string) return Opaque(desc);
        if (wrappedText) return @{@"kind": @"string", @"text": string, @"code": CodeString(type), @"data": Flatten(desc)};
        return @{@"kind": @"string", @"text": string};
    }
    if (type == typeAEList) {
        NSMutableArray *items = [NSMutableArray array];
        for (NSInteger i = 1; i <= desc.numberOfItems; i++) [items addObject:Decode([desc descriptorAtIndex:i], depth+1)];
        return @{@"kind": @"list", @"items": items};
    }
    if (type == typeAERecord) {
        NSMutableDictionary *fields = [NSMutableDictionary dictionary];
        for (NSInteger i = 1; i <= desc.numberOfItems; i++) {
            NSString *key = CodeString([desc keywordForDescriptorAtIndex:i]);
            if (!key) return Opaque(desc);
            fields[key] = Decode([desc descriptorAtIndex:i], depth+1);
        }
        return @{@"kind": @"record", @"fields": fields};
    }
    return Opaque(desc);
}

char *mail_ae_call(const char *json, int send_event, long timeout_ticks, int *attempted_out) {
    @autoreleasepool {
        BOOL attempted = NO;
        NSTimeInterval permissionMS = 0, dispatchMS = 0;
        BOOL permissionCached = NO;
        *attempted_out = 0;
        NSTimeInterval nativeDeadline = NSProcessInfo.processInfo.systemUptime + (double)timeout_ticks / 60.0;
        NSDictionary *output;
        @try {
            NSData *input = [NSData dataWithBytes:json length:strlen(json)];
            NSError *parseError = nil;
            NSDictionary *value = [NSJSONSerialization JSONObjectWithData:input options:0 error:&parseError];
            if (![value isKindOfClass:NSDictionary.class]) Fail(paramErr, @"Invalid Apple Event bridge input");
            NSAppleEventDescriptor *result;
            if (!send_event) {
                result = Encode(value, 0);
            } else {
                OSType eventClass = Code(value[@"class"]), eventID = Code(value[@"id"]);
                NSAppleEventDescriptor *target = [NSAppleEventDescriptor descriptorWithBundleIdentifier:@"com.apple.mail"];
                NSAppleEventDescriptor *event = [NSAppleEventDescriptor appleEventWithEventClass:eventClass eventID:eventID
                    targetDescriptor:target returnID:kAutoGenerateReturnID transactionID:kAnyTransactionID];
                NSDictionary *params = value[@"params"];
                for (NSString *key in params) [event setParamDescriptor:Encode(params[key], 0) forKeyword:Code(key)];
                if (@available(macOS 10.14, *)) {
                    NSTimeInterval permissionStart = NSProcessInfo.processInfo.systemUptime;
                    OSStatus permission = Preflight(target, eventClass, eventID, &permissionCached);
                    permissionMS = (NSProcessInfo.processInfo.systemUptime - permissionStart) * 1000.0;
                    if (permission != noErr) Fail(permission, @"Mail automation permission is unavailable; no consent prompt was requested");
                }
                long remainingTicks = (long)floor((nativeDeadline - NSProcessInfo.processInfo.systemUptime) * 60.0);
                if (remainingTicks < 1) Fail(errAETimeout, @"Apple Event deadline expired before dispatch");
                AppleEvent reply = {typeNull, NULL};
                // Ownership is transferred immediately after dispatch returns,
                // including the error path, so a partially populated reply is
                // disposed even when AESendMessage fails.
                attempted = YES;
                *attempted_out = 1;
                NSTimeInterval dispatchStart = NSProcessInfo.processInfo.systemUptime;
                OSStatus status = AESendMessage(event.aeDesc, &reply,
                    kAEWaitReply | kAENeverInteract | kAEDoNotPromptForUserConsent, (SInt32)remainingTicks);
                dispatchMS = (NSProcessInfo.processInfo.systemUptime - dispatchStart) * 1000.0;
                NSAppleEventDescriptor *response = [[NSAppleEventDescriptor alloc] initWithAEDescNoCopy:&reply];
                if (status == errAEEventNotPermitted || status == errAEEventWouldRequireUserConsent) InvalidatePermission(eventClass, eventID);
                if (status != noErr) Fail(status, status == errAETimeout ? @"Mail Apple Event timed out" : @"Mail Apple Event dispatch failed");
                NSAppleEventDescriptor *errorNumber = [response paramDescriptorForKeyword:keyErrorNumber];
                if (errorNumber && errorNumber.int32Value != 0) {
                    if (errorNumber.int32Value == errAEEventNotPermitted || errorNumber.int32Value == errAEEventWouldRequireUserConsent) InvalidatePermission(eventClass, eventID);
                    NSString *message = [response paramDescriptorForKeyword:keyErrorString].stringValue ?: @"Mail rejected the Apple Event";
                    Fail(errorNumber.int32Value, message);
                }
                result = [response paramDescriptorForKeyword:keyDirectObject];
            }
            output = @{@"value": Decode(result, 0), @"attempted": @(attempted)};
        } @catch (NSException *exception) {
            NSNumber *code = exception.userInfo[@"code"] ?: @(paramErr);
            output = @{@"failed": @YES, @"code": code, @"message": exception.reason ?: @"Native Apple Event exception", @"attempted": @(attempted)};
        }
        NSMutableDictionary *instrumented = [output mutableCopy];
        instrumented[@"permissionMS"] = @(permissionMS);
        instrumented[@"dispatchMS"] = @(dispatchMS);
        instrumented[@"permissionCached"] = @(permissionCached);
        output = instrumented;
        NSError *error = nil;
        NSData *data = [NSJSONSerialization dataWithJSONObject:output options:0 error:&error];
        if (!data) return strdup("{\"failed\":true,\"code\":-50,\"message\":\"Cannot encode native Apple Event response\"}");
        char *result = malloc(data.length + 1);
        if (!result) return NULL;
        memcpy(result, data.bytes, data.length);
        result[data.length] = 0;
        return result;
    }
}
