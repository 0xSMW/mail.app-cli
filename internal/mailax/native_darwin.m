//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
#include "native_darwin.h"

// Only public AX and NSRunningApplication APIs are used here. The bridge never
// compiles AppleScript/JXA, loads Mail internals, or operates on another window.
@interface MailArchiveAXSession : NSObject
@property(strong) id root;
@property(strong) id window;
@property(strong) id table;
@property(strong) NSArray *candidates;
@property(strong) id selected;
@property(strong) id archive;
@property(copy) NSString *windowID;
@property NSUInteger cursor;
@property NSTimeInterval lookupDeadline;
@property BOOL mainAttributeSet;
@property BOOL focusedAttributeSet;
@property BOOL rootFocusedWindowSet;
@end
@implementation MailArchiveAXSession
@end

static void Fail(NSString *message, NSInteger code) {
    @throw [NSException exceptionWithName:@"MailArchiveAX" reason:message userInfo:@{@"code":@(code)}];
}
static AXUIElementRef Element(id value) { return (__bridge AXUIElementRef)value; }
static _Thread_local NSTimeInterval operationDeadline;
int mail_ax_front_pid_valid(int front_status, int pid_status, int pid) {
    return front_status == noErr && pid_status == noErr && pid > 0;
}
static pid_t ProcessManagerFrontPID(void) {
    ProcessSerialNumber front = {0,0};
    pid_t pid = 0;
    // These are deprecated but remain public 64-bit SDK APIs. Their
    // replacement's time-varying AppKit state needs a main run loop; this
    // foreground read uses Process Manager without that cached AppKit state.
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
    OSErr status = GetFrontProcess(&front);
    if (status != noErr) Fail([NSString stringWithFormat:@"Cannot read the front process: %d",status],2);
    OSStatus pidStatus = GetProcessPID(&front,&pid);
#pragma clang diagnostic pop
    if (!mail_ax_front_pid_valid(status,pidStatus,pid)) Fail([NSString stringWithFormat:@"Cannot identify the front process PID: %d",pidStatus],2);
    return pid;
}
static id Attribute(id element, CFStringRef name, BOOL required) {
    if (NSProcessInfo.processInfo.systemUptime >= operationDeadline) Fail(@"Mail Accessibility operation timed out", 2);
    if (!element) { if (required) Fail(@"Mail Accessibility element is missing", 2); return nil; }
    CFTypeRef value = NULL;
    AXError status = AXUIElementCopyAttributeValue(Element(element), name, &value);
    if (status != kAXErrorSuccess) {
        if (required) Fail([NSString stringWithFormat:@"Mail Accessibility %@ failed: %d", (__bridge NSString *)name, status], 2);
        return nil;
    }
    return CFBridgingRelease(value);
}
static NSArray *ArrayAttribute(id element, CFStringRef name, BOOL required) {
    id value = Attribute(element, name, required);
    if (!value && !required) return @[];
    if (![value isKindOfClass:[NSArray class]]) Fail(@"Mail Accessibility collection has an invalid type", 2);
    return value;
}
static BOOL Same(id first, id second) { return first && second && CFEqual((__bridge CFTypeRef)first, (__bridge CFTypeRef)second); }
static BOOL HasID(id element, NSString *identifier) { return [Attribute(element, kAXIdentifierAttribute, NO) isEqual:identifier]; }
static id Find(id element, NSString *identifier, NSUInteger depth) {
    if (!element || depth > 8) return nil;
    if (HasID(element, identifier)) return element;
    if (HasID(element, @"MailboxesOutlineView")) return nil;
    for (id child in ArrayAttribute(element, kAXChildrenAttribute, NO)) {
        id found = Find(child, identifier, depth + 1);
        if (found) return found;
    }
    return nil;
}
static NSRunningApplication *FreshFrontApplication(void) {
    // NSWorkspace/NSRunningApplication time-varying properties are cached
    // until a main run-loop turn. A Go CLI has no AppKit main run loop. Read
    // the currently focused process through the public Process Manager API;
    // the bundle identifier and PID of its NSRunningApplication are fixed.
    if (NSProcessInfo.processInfo.systemUptime >= operationDeadline) Fail(@"Mail Accessibility operation timed out",2);
    pid_t pid = ProcessManagerFrontPID();
    NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
    if (!app) Fail(@"Cannot identify the focused running application", 2);
    return app;
}
static NSString *FrontApplication(void) {
    NSString *bundle = FreshFrontApplication().bundleIdentifier;
    if (![bundle isKindOfClass:[NSString class]] || !bundle.length) Fail(@"Cannot identify the previous foreground application", 2);
    return bundle;
}
static NSRunningApplication *MailApplication(void) {
    NSArray *apps = [NSRunningApplication runningApplicationsWithBundleIdentifier:@"com.apple.mail"];
    if (apps.count != 1) Fail(@"Mail must be running as exactly one application", 2);
    return apps.firstObject;
}
static id OwnedWindow(MailArchiveAXSession *s, BOOL ready) {
    NSMutableArray *matches = [NSMutableArray array];
    for (id win in ArrayAttribute(s.root, kAXWindowsAttribute, YES)) {
        if (HasID(win, s.windowID)) [matches addObject:win];
    }
    if (matches.count != 1) Fail(@"Owned Mail viewer window is unavailable or ambiguous", ready && matches.count == 0 ? 1 : 2);
    if (s.window && !Same(s.window, matches.firstObject)) Fail(@"Owned Mail viewer window changed", 2);
    return matches.firstObject;
}
static BOOL OwnedFocus(MailArchiveAXSession *s) {
    if (![FrontApplication() isEqual:@"com.apple.mail"]) return NO;
    id focused = Attribute(s.root, kAXFocusedWindowAttribute, NO);
    return HasID(focused, s.windowID) && Same(focused, s.window);
}
static BOOL SetWritableFocusAttribute(id element, CFStringRef attribute, CFTypeRef value) {
    if (NSProcessInfo.processInfo.systemUptime >= operationDeadline) Fail(@"Mail Accessibility operation timed out",2);
    Boolean writable = false;
    AXError status = AXUIElementIsAttributeSettable(Element(element),attribute,&writable);
    if (status == kAXErrorAttributeUnsupported) return NO;
    if (status != kAXErrorSuccess) Fail([NSString stringWithFormat:@"Mail focus attribute %@ availability failed: %d",(__bridge NSString *)attribute,status],2);
    if (!writable) return NO;
    status = AXUIElementSetAttributeValue(Element(element),attribute,value);
    if (status != kAXErrorSuccess) Fail([NSString stringWithFormat:@"Mail focus attribute %@ failed: %d",(__bridge NSString *)attribute,status],2);
    return YES;
}
static BOOL RequestOwnedFocus(MailArchiveAXSession *s) {
    OwnedWindow(s,NO);
    // Do not set a window's focus attributes before activation has actually
    // reached Mail. NSRunningApplication's activation reply is asynchronous.
    if (FreshFrontApplication().processIdentifier != MailApplication().processIdentifier) return NO;
    s.mainAttributeSet = SetWritableFocusAttribute(s.window,kAXMainAttribute,kCFBooleanTrue);
    s.focusedAttributeSet = SetWritableFocusAttribute(s.window,kAXFocusedAttribute,kCFBooleanTrue);
    s.rootFocusedWindowSet = SetWritableFocusAttribute(s.root,kAXFocusedWindowAttribute,(__bridge CFTypeRef)s.window);
    if (FreshFrontApplication().processIdentifier != MailApplication().processIdentifier) Fail(@"Mail focus changed during owned viewer preparation",2);
    AXError raised = AXUIElementPerformAction(Element(s.window),kAXRaiseAction);
    if (raised != kAXErrorSuccess) Fail(@"Could not raise the exact owned Mail viewer after focusing it",2);
    return YES;
}
static NSDictionary *FocusDiagnostic(MailArchiveAXSession *s, NSString *op) {
    // Diagnostic collection cannot authorize an action. It is opt-in, carries
    // only process/window metadata, and never reads window titles or message data.
    NSMutableDictionary *result = [NSMutableDictionary dictionaryWithDictionary:@{@"operation":op ?: @"", @"expectedWindowID":s.windowID ?: @""}];
    @try {
        result[@"workspaceFrontPID"] = @(NSWorkspace.sharedWorkspace.frontmostApplication.processIdentifier);
        NSRunningApplication *front = FreshFrontApplication();
        result[@"frontPID"] = @(front.processIdentifier);
        result[@"frontSource"] = @"process-manager";
        result[@"frontAXStatus"] = @0;
        result[@"axFrontPID"] = @0;
        result[@"mailPID"] = @(MailApplication().processIdentifier);
        if (s.root) {
            id focused = Attribute(s.root, kAXFocusedWindowAttribute, NO);
            result[@"focusedIdentifier"] = Attribute(focused, kAXIdentifierAttribute, NO) ?: @"";
            result[@"focusedRole"] = Attribute(focused, kAXRoleAttribute, NO) ?: @"";
            result[@"ownedIdentifierMatches"] = @(HasID(focused, s.windowID));
            result[@"sameWindow"] = @(Same(focused, s.window));
            result[@"mailFrontmostAX"] = @([Attribute(s.root, kAXFrontmostAttribute, NO) isEqual:@YES]);
            result[@"mainAttributeSet"] = @(s.mainAttributeSet);
            result[@"focusedAttributeSet"] = @(s.focusedAttributeSet);
            result[@"rootFocusedWindowSet"] = @(s.rootFocusedWindowSet);
            NSMutableArray *identifiers = [NSMutableArray array];
            for (id window in ArrayAttribute(s.root,kAXWindowsAttribute,NO)) {
                if (identifiers.count >= 32) break;
                [identifiers addObject:Attribute(window,kAXIdentifierAttribute,NO) ?: @""];
            }
            result[@"windowIDs"] = identifiers;
        }
    } @catch (NSException *exception) {
        result[@"incomplete"] = @YES;
    }
    return result;
}
static void Guard(MailArchiveAXSession *s) {
    OwnedWindow(s, NO);
    if (!OwnedFocus(s)) Fail(@"Mail focus changed", 2);
    NSArray *selected = ArrayAttribute(s.table, kAXSelectedRowsAttribute, YES);
    if (selected.count != 1 || !Same(selected.firstObject, s.selected)) Fail(@"Mail message selection changed", 2);
    if (Find(s.selected, @"Mail.messageList.cell.view.disclosureButton", 0)) Fail(@"Conversation archive is not allowed for an individual message", 2);
}
static void CollectArchive(id element, NSUInteger depth, NSMutableArray *matches) {
    if (!element || depth > 4) return;
    if (HasID(element, @"Mail.menuBar.messageMenu.archive")) [matches addObject:element];
    for (id child in ArrayAttribute(element, kAXChildrenAttribute, NO)) CollectArchive(child, depth+1, matches);
}
static id ArchiveControl(MailArchiveAXSession *s) {
    id bar = Attribute(s.root, kAXMenuBarAttribute, YES);
    NSMutableArray *menus = [NSMutableArray array];
    for (id top in ArrayAttribute(bar, kAXChildrenAttribute, YES)) {
        if (HasID(top, @"Mail.menuBar.messageMenu")) [menus addObject:top];
    }
    if (menus.count != 1) Fail(@"Mail Message menu is unavailable or ambiguous", 2);
    NSMutableArray *matches = [NSMutableArray array];
    CollectArchive(menus.firstObject, 0, matches);
    if (matches.count != 1) Fail(@"Mail native Archive control is unavailable or ambiguous", 2);
    id control = matches.firstObject;
    if (![Attribute(control, kAXRoleAttribute, YES) isEqual:(__bridge NSString *)kAXMenuItemRole] ||
        ![Attribute(control, kAXEnabledAttribute, YES) isEqual:@YES]) Fail(@"Mail native Archive is disabled", 2);
    NSArray *actions = nil;
    CFArrayRef copied = NULL;
    AXError status = AXUIElementCopyActionNames(Element(control), &copied);
    actions = CFBridgingRelease(copied);
    if (status != kAXErrorSuccess || ![actions containsObject:(__bridge NSString *)kAXPressAction]) Fail(@"Mail native Archive cannot be pressed", 2);
    return control;
}
static NSArray *Candidates(id table) {
    NSArray *rows = ArrayAttribute(table, kAXRowsAttribute, YES);
    NSArray *selected = ArrayAttribute(table, kAXSelectedRowsAttribute, YES);
    NSMutableArray<NSNumber *> *hints = [NSMutableArray array];
    for (id selection in selected) {
        for (NSUInteger index=0; index<rows.count; index++) {
            if (Same(rows[index], selection)) { [hints addObject:@(index)]; break; }
        }
    }
    NSMutableIndexSet *seen = [NSMutableIndexSet indexSet];
    NSMutableArray *candidates = [NSMutableArray array];
    void (^add)(NSInteger) = ^(NSInteger index) {
        if (index >= 0 && index < (NSInteger)rows.count && ![seen containsIndex:index]) {
            [seen addIndex:index]; [candidates addObject:rows[index]];
        }
    };
    for (NSInteger distance=0; distance<40; distance++) for (NSNumber *hint in hints) { add(hint.integerValue + distance); add(hint.integerValue - distance); }
    for (NSUInteger index=0; index<MIN(rows.count,80); index++) add(index);
    return candidates;
}

char *mail_ax_call(const char *input, uintptr_t *handle, int *attempted) {
    @autoreleasepool {
        *attempted = 0;
        operationDeadline = NSProcessInfo.processInfo.systemUptime + 5;
        NSDictionary *reply;
        NSDictionary *request = nil;
        NSString *op = nil;
        MailArchiveAXSession *s = nil;
        @try {
            NSData *data = [[NSString stringWithUTF8String:input] dataUsingEncoding:NSUTF8StringEncoding];
            request = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
            NSNumber *budget = request[@"budgetMs"];
            if (budget) operationDeadline = MIN(operationDeadline, NSProcessInfo.processInfo.systemUptime + MAX(0.001,budget.doubleValue/1000.0));
            op = request[@"op"];
            s = *handle ? (__bridge MailArchiveAXSession *)(void *)*handle : nil;
            if ([op isEqual:@"release"]) {
                if (*handle) CFRelease((CFTypeRef)(void *)*handle);
                *handle = 0;
                reply = @{};
            } else {
                if (!AXIsProcessTrusted()) Fail(@"Native Gmail archive requires Accessibility access for the app running mail-app-cli", 2);
                if ([op isEqual:@"trusted"]) reply = @{};
                else if ([op isEqual:@"front"]) reply = @{@"text":FrontApplication()};
                else if ([op isEqual:@"focus"]) {
                    long long viewer = [request[@"viewer"] longLongValue];
                    if (viewer <= 0) Fail(@"Invalid owned Mail viewer", 2);
                    s = [MailArchiveAXSession new];
                    s.windowID = [NSString stringWithFormat:@"Mail.messageViewer.window.%lld", viewer];
                    s.root = CFBridgingRelease(AXUIElementCreateApplication(MailApplication().processIdentifier));
                    AXUIElementSetMessagingTimeout(Element(s.root), 0.5);
                    s.window = OwnedWindow(s, NO);
                    reply = @{@"value":@(OwnedFocus(s))};
                }
                else if ([op isEqual:@"restore"]) {
                    NSString *bundle = request[@"bundle"];
                    // Never steal focus after the user switched to another app.
                    if ([FrontApplication() isEqual:@"com.apple.mail"] && bundle.length && ![bundle isEqual:@"com.apple.mail"]) {
                        NSArray *apps = [NSRunningApplication runningApplicationsWithBundleIdentifier:bundle];
                        if (apps.count == 1 && ![apps.firstObject activateWithOptions:0]) Fail(@"Could not restore the previous application", 2);
                    }
                    reply = @{};
                } else if ([op isEqual:@"prepare"]) {
                    long long viewer = [request[@"viewer"] longLongValue];
                    if (viewer <= 0 || *handle) Fail(@"Invalid owned Mail viewer", 2);
                    s = [MailArchiveAXSession new];
                    s.windowID = [NSString stringWithFormat:@"Mail.messageViewer.window.%lld", viewer];
                    s.root = CFBridgingRelease(AXUIElementCreateApplication(MailApplication().processIdentifier));
                    AXUIElementSetMessagingTimeout(Element(s.root), 0.5);
                    s.window = OwnedWindow(s, YES);
                    s.table = Find(s.window, @"Mail.messageList", 0);
                    if (!s.table) Fail(@"Owned Mail message list did not become ready", 1);
                    if (![MailApplication() activateWithOptions:0]) Fail(@"Could not focus Mail", 2);
                    s.candidates = Candidates(s.table);
                    s.lookupDeadline = NSProcessInfo.processInfo.systemUptime + 5;
                    *handle = (uintptr_t)CFBridgingRetain(s);
                    reply = @{};
                } else {
                    if (!s) Fail(@"Mail Accessibility session is unavailable", 2);
                    if ([op isEqual:@"focusowned"]) reply = @{@"value":@(RequestOwnedFocus(s))};
                    else if ([op isEqual:@"ownedfocus"]) {
                        if (budget) AXUIElementSetMessagingTimeout(Element(s.root), MAX(0.001,MIN(0.5,budget.doubleValue/1000.0)));
                        BOOL owned = NO;
                        @try { owned = OwnedFocus(s); }
                        @finally { if (budget) AXUIElementSetMessagingTimeout(Element(s.root), 0.5); }
                        reply = @{@"value":@(owned)};
                    }
                    else if ([op isEqual:@"guard"]) { Guard(s); reply = @{}; }
                    else if ([op isEqual:@"archive"]) { Guard(s); s.archive = ArchiveControl(s); reply = @{}; }
                    else if ([op isEqual:@"select"]) {
                        OwnedWindow(s, NO);
                        if (!OwnedFocus(s)) Fail(@"Mail focus changed", 2);
                        NSString *subject = request[@"subject"];
                        BOOL found = NO;
                        while (s.cursor < s.candidates.count && NSProcessInfo.processInfo.systemUptime < s.lookupDeadline) {
                            id row = s.candidates[s.cursor++];
                            id label = Find(row, @"Mail.messageList.cell.view.subjectLabel", 0);
                            if (!label || ![Attribute(label, kAXValueAttribute, YES) isEqual:subject]) continue;
                            if (Find(row, @"Mail.messageList.cell.view.disclosureButton", 0)) continue;
                            AXError status = AXUIElementSetAttributeValue(Element(s.table), kAXSelectedRowsAttribute, (__bridge CFTypeRef)@[row]);
                            if (status != kAXErrorSuccess) Fail(@"Could not select exact Mail row", 2);
                            s.selected = row; found = YES; break;
                        }
                        reply = @{@"value":@(found)};
                    } else if ([op isEqual:@"press"]) {
                        Guard(s);
                        id archive = ArchiveControl(s);
                        if (!s.archive || !Same(archive, s.archive)) Fail(@"Mail Archive control changed", 2);
                        *attempted = 1;
                        AXError status = AXUIElementPerformAction(Element(archive), kAXPressAction);
                        if (status != kAXErrorSuccess) Fail([NSString stringWithFormat:@"Mail Archive action returned %d; do not retry", status], 2);
                        reply = @{};
                    } else Fail(@"Unknown Mail Accessibility operation", 2);
                }
            }
        } @catch (NSException *exception) {
            reply = @{@"error":exception.reason ?: @"Mail Accessibility failed", @"code":exception.userInfo[@"code"] ?: @2};
        }
        if ([request[@"diagnostic"] boolValue] && ![op isEqual:@"release"] &&
            ([op isEqual:@"front"] || [op isEqual:@"prepare"] || [op isEqual:@"focusowned"] || [op isEqual:@"ownedfocus"] || [op isEqual:@"guard"] || [op isEqual:@"focus"])) {
            // Keep diagnostics separate from the action deadline and result.
            operationDeadline = NSProcessInfo.processInfo.systemUptime + 0.25;
            NSMutableDictionary *annotated = [reply mutableCopy];
            annotated[@"diagnostic"] = FocusDiagnostic(s,op);
            reply = annotated;
        }
        NSData *encoded = [NSJSONSerialization dataWithJSONObject:reply options:0 error:nil];
        return encoded ? strdup([[NSString alloc] initWithData:encoded encoding:NSUTF8StringEncoding].UTF8String) : NULL;
    }
}
