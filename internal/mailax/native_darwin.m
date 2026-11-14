//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
#include "native_darwin.h"
#include <math.h>

// Only public AX and NSRunningApplication APIs are used here. The bridge never
// compiles AppleScript/JXA, loads Mail internals, or operates on another window.
@interface MailArchiveAXSession : NSObject
@property(strong) id root;
@property(strong) id window;
@property(strong) id table;
@property(strong) NSMutableArray *tried;
@property(strong) id selected;
@property(strong) id archive;
@property(copy) NSString *windowID;
@property(strong) NSMutableArray *expanded;
@property(strong) id scrollArea;
@property(strong) id scrollBar;
@property(strong) NSArray *beforeRows;
@property double beforeValue;
@property NSUInteger pages, expansions, pendingPolls, quietPolls;
@property BOOL scrollStarted, pendingScroll, scrollFinished, pendingProgress;
@property(copy) NSString *pendingAction;
@property(strong) id pendingTarget;
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
// All mutation authority is derived from the owned window's current tree.
// The foreground application and Mail's key/main windows are irrelevant.
static void CollectID(id element, NSString *identifier, NSUInteger depth, NSMutableArray *matches) {
    if (!element || depth > 8) return;
    id currentID = Attribute(element, kAXIdentifierAttribute, NO);
    if ([currentID isEqual:identifier]) { [matches addObject:element]; return; }
    // Window-level discovery must never enumerate message rows or sidebar
    // contents. Continue through sibling branches to reject duplicate controls.
    if ([currentID isEqual:@"Mail.messageList"] || [currentID isEqual:@"MailboxesOutlineView"]) return;
    for (id child in ArrayAttribute(element, kAXChildrenAttribute, NO)) CollectID(child, identifier, depth+1, matches);
}
static id OwnedTable(MailArchiveAXSession *s, BOOL ready) {
    id window = OwnedWindow(s, ready);
    NSMutableArray *matches = [NSMutableArray array];
    CollectID(window, @"Mail.messageList", 0, matches);
    if (matches.count != 1) Fail(@"Owned Mail message list is unavailable or ambiguous", ready && matches.count == 0 ? 1 : 2);
    id table = matches.firstObject;
    if (s.table && !Same(s.table, table)) Fail(@"Owned Mail message list changed", 2);
    return table;
}
static void RequireRowMembership(MailArchiveAXSession *s, id row) {
    NSUInteger matches = 0;
    for (id current in ArrayAttribute(s.table, kAXRowsAttribute, YES)) if (Same(current, row)) matches++;
    if (matches != 1) Fail(@"Owned Mail row is unavailable or ambiguous", 2);
}
static BOOL Conversation(MailArchiveAXSession *s, id row) {
    if (Find(row,@"Mail.messageList.cell.view.disclosureButton",0) || ArrayAttribute(row,kAXDisclosedRowsAttribute,NO).count) return YES;
    id disclosing=Attribute(row,kAXDisclosingAttribute,NO);
    if (!disclosing) return NO;
    if (![disclosing isEqual:@NO]) return YES;
    // Some outlines expose AXDisclosing=false on a leaf. Accept that only
    // when reciprocal disclosure metadata proves it is an individual child.
    id parent=Attribute(row,kAXDisclosedByRowAttribute,NO);
    if (!parent || ![Attribute(parent,kAXDisclosingAttribute,NO) isEqual:@YES]) return YES;
    RequireRowMembership(s,parent);
    NSUInteger matches = 0;
    for (id child in ArrayAttribute(parent, kAXDisclosedRowsAttribute, NO)) if (Same(child,row)) matches++;
    id level=Attribute(row,kAXDisclosureLevelAttribute,NO), parentLevel=Attribute(parent,kAXDisclosureLevelAttribute,NO);
    return matches!=1 || ![level isKindOfClass:[NSNumber class]] || ![parentLevel isKindOfClass:[NSNumber class]] || [level integerValue]<=[parentLevel integerValue];
}
static void Guard(MailArchiveAXSession *s) {
    OwnedTable(s, NO);
    RequireRowMembership(s, s.selected);
    NSArray *selected = ArrayAttribute(s.table, kAXSelectedRowsAttribute, YES);
    if (selected.count != 1 || !Same(selected.firstObject, s.selected)) Fail(@"Mail message selection changed", 2);
    if (Conversation(s,s.selected)) Fail(@"Conversation archive is not allowed for an individual message", 2);
}
static void CollectToolbar(id element, NSUInteger depth, NSMutableArray *matches) {
    if (!element || depth > 8) return;
    id currentID = Attribute(element, kAXIdentifierAttribute, NO);
    if ([currentID isEqual:@"Mail.messageList"] || [currentID isEqual:@"MailboxesOutlineView"]) return;
    if ([Attribute(element, kAXRoleAttribute, NO) isEqual:(__bridge NSString *)kAXToolbarRole]) { [matches addObject:element]; return; }
    for (id child in ArrayAttribute(element, kAXChildrenAttribute, NO)) CollectToolbar(child, depth+1, matches);
}
static void CollectArchiveButton(id element, NSString *title, NSUInteger depth, NSMutableArray *matches) {
    if (!element || depth > 5) return;
    if ([Attribute(element, kAXRoleAttribute, NO) isEqual:(__bridge NSString *)kAXButtonRole] &&
        [Attribute(element, kAXDescriptionAttribute, NO) isEqual:title]) { [matches addObject:element]; return; }
    for (id child in ArrayAttribute(element, kAXChildrenAttribute, NO)) CollectArchiveButton(child, title, depth+1, matches);
}
static id ArchiveControl(MailArchiveAXSession *s) {
    id window = OwnedWindow(s, NO);
    // The global menu supplies a localized title only. Its enabled state and
    // responder-chain target never authorize an action on the owned viewer.
    id bar = Attribute(s.root, kAXMenuBarAttribute, YES);
    NSMutableArray *menuItems = [NSMutableArray array];
    CollectID(bar, @"Mail.menuBar.messageMenu.archive", 0, menuItems);
    if (menuItems.count != 1 || ![Attribute(menuItems.firstObject, kAXRoleAttribute, YES) isEqual:(__bridge NSString *)kAXMenuItemRole]) Fail(@"Mail Archive title is unavailable or ambiguous", 2);
    id title = Attribute(menuItems.firstObject, kAXTitleAttribute, YES);
    if (![title isKindOfClass:[NSString class]] || ![title length]) Fail(@"Mail Archive title is unavailable", 2);
    NSMutableArray *toolbars = [NSMutableArray array];
    CollectToolbar(window, 0, toolbars);
    if (toolbars.count != 1) Fail(@"Owned Mail toolbar is unavailable or ambiguous", 2);
    NSMutableArray *matches = [NSMutableArray array];
    CollectArchiveButton(toolbars.firstObject, title, 0, matches);
    if (matches.count != 1) Fail(@"Owned Mail Archive button is unavailable or ambiguous", 2);
    id control = matches.firstObject;
    if (![Attribute(control, kAXEnabledAttribute, YES) isEqual:@YES]) Fail(@"Owned Mail Archive button is disabled", 2);
    CFArrayRef copied = NULL;
    AXError status = AXUIElementCopyActionNames(Element(control), &copied);
    NSArray *actions = CFBridgingRelease(copied);
    if (status != kAXErrorSuccess || ![actions containsObject:(__bridge NSString *)kAXPressAction]) Fail(@"Owned Mail Archive button cannot be pressed", 2);
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
    // Hints are an optimization only; every exposed row remains eligible.
    for (NSUInteger index=0; index<rows.count; index++) add(index);
    return candidates;
}

// Navigation is confined to a verified parent/children chain from the exact
// owned table to its window. Never infer ownership from role or focus alone.
static BOOL ChildOnce(id parent, id child) {
    NSUInteger count = 0;
    for (id item in ArrayAttribute(parent,kAXChildrenAttribute,YES)) if (Same(item,child)) count++;
    return count == 1;
}
static id OwnedScroll(MailArchiveAXSession *s) {
    OwnedTable(s,NO);
    id child = s.table, area = nil;
    for (NSUInteger depth=0; depth<8; depth++) {
        id parent = Attribute(child,kAXParentAttribute,NO);
        if (!parent) return nil;
        if (!ChildOnce(parent,child)) Fail(@"Owned Mail scroll ancestry changed",2);
        if (!area && [Attribute(parent,kAXRoleAttribute,NO) isEqual:(__bridge NSString *)kAXScrollAreaRole]) area = parent;
        if (Same(parent,s.window)) {
            if (!area) return nil;
            if (s.scrollArea && !Same(area,s.scrollArea)) Fail(@"Owned Mail scroll area changed",2);
            id bar = Attribute(area,kAXVerticalScrollBarAttribute,NO);
            if (!bar) return nil;
            if (![Attribute(bar,kAXRoleAttribute,YES) isEqual:(__bridge NSString *)kAXScrollBarRole] ||
                !Same(Attribute(bar,kAXParentAttribute,YES),area) || !ChildOnce(area,bar)) Fail(@"Owned Mail scroll bar provenance is invalid",2);
            if (s.scrollBar && !Same(bar,s.scrollBar)) Fail(@"Owned Mail scroll bar changed",2);
            s.scrollArea = area;
            s.scrollBar = bar;
            return bar;
        }
        child = parent;
    }
    Fail(@"Owned Mail scroll ancestry exceeded its bound",2); return nil;
}
static BOOL Settable(id element, CFStringRef key) {
    Boolean settable = false;
    return AXUIElementIsAttributeSettable(Element(element),key,&settable) == kAXErrorSuccess && settable;
}
static BOOL Supports(id element, CFStringRef action) {
    CFArrayRef copied = NULL;
    AXError status = AXUIElementCopyActionNames(Element(element),&copied);
    NSArray *actions = CFBridgingRelease(copied);
    return status == kAXErrorSuccess && [actions containsObject:(__bridge NSString *)action];
}
// Never blind-toggle a disclosure. A public press fallback is allowed only
// with explicit collapsed state and a unique control proven inside this row.
static id ExpansionControl(id row) {
    NSMutableArray *controls = [NSMutableArray array];
    CollectID(row, @"Mail.messageList.cell.view.disclosureButton", 0, controls);
    if (controls.count != 1) return nil;
    id control = controls.firstObject;
    id state = Attribute(row, kAXDisclosingAttribute, NO);
    id value = Attribute(control, kAXValueAttribute, NO);
    if (!([state isEqual:@NO] || (!state && [value isEqual:@NO])) || [value isEqual:@YES]) return nil;
    if (![Attribute(control, kAXEnabledAttribute, NO) isEqual:@YES] || !Supports(control, kAXPressAction)) return nil;
    id child = control;
    for (NSUInteger depth = 0; depth < 8; depth++) {
        id parent = Attribute(child, kAXParentAttribute, NO);
        if (!parent || !ChildOnce(parent, child)) return nil;
        if (Same(parent, row)) return control;
        child = parent;
    }
    return nil;
}
static BOOL CanExpand(id row) {
    return ([Attribute(row, kAXDisclosingAttribute, NO) isEqual:@NO] && Settable(row, kAXDisclosingAttribute)) || ExpansionControl(row) != nil;
}
static void NavigationDeadline(void) {
    if (NSProcessInfo.processInfo.systemUptime >= operationDeadline) Fail(@"Mail traversal timed out before action dispatch", 2);
}
static double ScrollNumber(id bar, CFStringRef key) {
    id value = Attribute(bar,key,YES);
    if (![value isKindOfClass:[NSNumber class]] || !isfinite([value doubleValue])) Fail(@"Owned Mail scroll position is invalid",2);
    return [value doubleValue];
}
static void PageControls(id node, NSUInteger depth, NSMutableArray *matches) {
    if (depth>3) return;
    for (id child in ArrayAttribute(node,kAXChildrenAttribute,NO)) {
        if (!Same(Attribute(child,kAXParentAttribute,YES),node)) Fail(@"Owned Mail page control ancestry changed",2);
        if ([Attribute(child,kAXSubroleAttribute,NO) isEqual:(__bridge NSString *)kAXIncrementPageSubrole]) [matches addObject:child];
        PageControls(child,depth+1,matches);
    }
}
static BOOL SameRows(NSArray *left, NSArray *right) {
    if (left.count != right.count) return NO;
    for (NSUInteger i=0;i<left.count;i++) if (!Same(left[i],right[i])) return NO;
    return YES;
}
// Discovery and dispatch are separate steps. The caller performs a fresh Mail
// preview/account check between them, after potentially expensive row scanning.
static BOOL DispatchNavigation(MailArchiveAXSession *s) {
    NSString *action = s.pendingAction;
    id target = s.pendingTarget;
    s.pendingAction = nil;
    s.pendingTarget = nil;
    OwnedTable(s, NO);
    if ([action isEqual:@"expand"] || [action isEqual:@"select"]) {
        RequireRowMembership(s, target);
        if ([action isEqual:@"expand"]) {
            if (!Conversation(s, target) || !CanExpand(target)) return NO;
            if ([Attribute(target, kAXDisclosingAttribute, NO) isEqual:@NO] && Settable(target, kAXDisclosingAttribute)) {
                NavigationDeadline();
                if (AXUIElementSetAttributeValue(Element(target), kAXDisclosingAttribute, kCFBooleanTrue) != kAXErrorSuccess) Fail(@"Could not expand owned Mail conversation", 2);
            } else {
                id control = ExpansionControl(target);
                if (!control) Fail(@"Owned Mail disclosure control changed", 2);
                NavigationDeadline();
                if (AXUIElementPerformAction(Element(control), kAXPressAction) != kAXErrorSuccess) Fail(@"Could not expand owned Mail conversation", 2);
            }
            [s.expanded addObject:target];
            s.expansions++;
            s.quietPolls = 0;
            return NO;
        }
        if (Conversation(s, target)) Fail(@"Mail candidate became a conversation", 2);
        NavigationDeadline();
        if (AXUIElementSetAttributeValue(Element(s.table), kAXSelectedRowsAttribute, (__bridge CFTypeRef)@[target]) != kAXErrorSuccess) Fail(@"Could not select exact Mail row", 2);
        [s.tried addObject:target];
        s.selected = target;
        return YES;
    }
    id bar = OwnedScroll(s);
    if (!bar || ScrollNumber(bar, kAXValueAttribute) != s.beforeValue) return NO;
    if ([action isEqual:@"reset"]) {
        if (!Same(target, bar) || !Settable(bar, kAXValueAttribute)) Fail(@"Owned Mail scroll bar changed", 2);
        double minimum = ScrollNumber(bar, kAXMinValueAttribute);
        NavigationDeadline();
        if (AXUIElementSetAttributeValue(Element(bar), kAXValueAttribute, (__bridge CFTypeRef)@(minimum)) != kAXErrorSuccess) Fail(@"Could not reset owned Mail scroll position", 2);
    } else {
        CFStringRef press = kAXPressAction;
        if (Same(target, bar)) press = kAXIncrementAction;
        else {
            NSMutableArray *controls = [NSMutableArray array];
            PageControls(bar, 0, controls);
            if (controls.count != 1 || !Same(controls.firstObject, target)) Fail(@"Owned Mail page control changed", 2);
        }
        if (![Attribute(target, kAXEnabledAttribute, YES) isEqual:@YES] || !Supports(target, press)) Fail(@"Owned Mail page control is unavailable", 2);
        NavigationDeadline();
        if (AXUIElementPerformAction(Element(target), press) != kAXErrorSuccess) Fail(@"Could not advance owned Mail message list", 2);
        OwnedScroll(s);
        if (!Same(target, bar)) {
            NSMutableArray *controls = [NSMutableArray array];
            PageControls(bar, 0, controls);
            if (controls.count != 1 || !Same(controls.firstObject, target)) Fail(@"Owned Mail page control changed", 2);
        }
    }
    s.scrollStarted = YES;
    s.pages++;
    s.pendingScroll = YES;
    s.pendingProgress = NO;
    s.pendingPolls = 0;
    s.quietPolls = 0;
    return NO;
}
static void QueueNavigation(MailArchiveAXSession *s, NSString *action, id target) {
    s.pendingAction = action;
    s.pendingTarget = target;
}
static BOOL SelectStep(MailArchiveAXSession *s, NSString *subject) {
    if (s.pendingAction) return DispatchNavigation(s);
    OwnedTable(s, NO);
    NSArray *rows = ArrayAttribute(s.table, kAXRowsAttribute, YES);
    if (rows.count > 10000 || s.tried.count > 10000) Fail(@"Owned Mail row traversal limit reached", 2);
    if (s.pendingScroll) {
        id bar = OwnedScroll(s);
        if (!bar) Fail(@"Owned Mail scroll bar disappeared", 2);
        double value = ScrollNumber(bar, kAXValueAttribute);
        if (value != s.beforeValue || !SameRows(rows, s.beforeRows)) {
            s.pendingProgress = YES;
            s.beforeValue = value;
            s.beforeRows = rows;
            s.pendingPolls = 0;
            return NO;
        }
        if (++s.pendingPolls < 6) return NO;
        s.pendingScroll = NO;
        if (s.pendingProgress) {
            // Virtualized AX row objects may now represent different messages.
            [s.tried removeAllObjects];
            [s.expanded removeAllObjects];
        } else {
            s.scrollFinished = YES;
        }
    }
    for (id row in Candidates(s.table)) {
        if (Conversation(s, row)) {
            if (CanExpand(row)) {
                BOOL tried = NO;
                for (id previous in s.expanded) if (Same(previous, row)) { tried = YES; break; }
                if (!tried) {
                    if (s.expansions >= 128) Fail(@"Owned Mail conversation expansion limit reached", 2);
                    QueueNavigation(s, @"expand", row);
                    return NO;
                }
            }
            continue; // A parent is never an individual archive candidate.
        }
        BOOL tried = NO;
        for (id previous in s.tried) if (Same(previous, row)) { tried = YES; break; }
        if (tried) continue;
        id label = Find(row, @"Mail.messageList.cell.view.subjectLabel", 0);
        if (!label || ![Attribute(label, kAXValueAttribute, YES) isEqual:subject]) continue;
        QueueNavigation(s, @"select", row);
        return NO;
    }
    // Wait for asynchronously disclosed children before leaving the viewport.
    if (++s.quietPolls < 6 || s.scrollFinished) return NO;
    id bar = OwnedScroll(s);
    if (!bar) { s.scrollFinished = YES; return NO; }
    double value = ScrollNumber(bar, kAXValueAttribute);
    double minimum = ScrollNumber(bar, kAXMinValueAttribute);
    double maximum = ScrollNumber(bar, kAXMaxValueAttribute);
    if (minimum >= maximum || value < minimum || value > maximum) Fail(@"Owned Mail scroll range is invalid", 2);
    if (s.pages >= 64) Fail(@"Owned Mail scroll page limit reached", 2);
    s.beforeRows = rows;
    s.beforeValue = value;
    if (!s.scrollStarted && value > minimum) {
        if (!Settable(bar, kAXValueAttribute)) { s.scrollFinished = YES; return NO; }
        QueueNavigation(s, @"reset", bar);
        return NO;
    }
    s.scrollStarted = YES;
    if (value >= maximum) { s.scrollFinished = YES; return NO; }
    NSMutableArray *controls = [NSMutableArray array];
    PageControls(bar, 0, controls);
    if (controls.count > 1) Fail(@"Owned Mail page control is ambiguous", 2);
    id control = controls.firstObject;
    CFStringRef action = kAXPressAction;
    if (!control && Supports(bar, kAXIncrementAction)) { control = bar; action = kAXIncrementAction; }
    if (!control || ![Attribute(control, kAXEnabledAttribute, YES) isEqual:@YES] || !Supports(control, action)) {
        s.scrollFinished = YES;
        return NO;
    }
    // Page/line increments preserve contiguous viewport progression. Arbitrary
    // scrollbar fractions could skip rows and are never used for forward travel.
    QueueNavigation(s, @"page", control);
    return NO;
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
                else if ([op isEqual:@"prepare"]) {
                    long long viewer = [request[@"viewer"] longLongValue];
                    if (viewer <= 0 || *handle) Fail(@"Invalid owned Mail viewer", 2);
                    s = [MailArchiveAXSession new];
                    s.windowID = [NSString stringWithFormat:@"Mail.messageViewer.window.%lld", viewer];
                    s.root = CFBridgingRelease(AXUIElementCreateApplication(MailApplication().processIdentifier));
                    AXUIElementSetMessagingTimeout(Element(s.root), 0.5);
                    s.window = OwnedWindow(s, YES);
                    s.table = OwnedTable(s, YES);
                    s.tried = [NSMutableArray array];
                    s.expanded = [NSMutableArray array];
                    *handle = (uintptr_t)CFBridgingRetain(s);
                    reply = @{};
                } else {
                    if (!s) Fail(@"Mail Accessibility session is unavailable", 2);
                    if ([op isEqual:@"guard"]) { Guard(s); reply = @{}; }
                    else if ([op isEqual:@"archive"]) { Guard(s); s.archive = ArchiveControl(s); reply = @{}; }
                    else if ([op isEqual:@"select"]) {
                        BOOL found=SelectStep(s,request[@"subject"]);
                        reply = @{@"value":@(found),@"exhausted":@(s.scrollFinished && !found)};
                    } else if ([op isEqual:@"press"]) {
                        Guard(s);
                        id archive = ArchiveControl(s);
                        if (!s.archive || !Same(archive, s.archive)) Fail(@"Mail Archive control changed", 2);
                        Guard(s);
                        if (NSProcessInfo.processInfo.systemUptime >= operationDeadline) Fail(@"Mail Accessibility operation timed out before Archive dispatch", 2);
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
            ([op isEqual:@"prepare"] || [op isEqual:@"guard"])) {
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
