#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>

@interface FixtureNode : NSObject
@property NSMutableDictionary *attributes;
@property NSString *name;
@end
@implementation FixtureNode
@end
static FixtureNode *fixtureTable, *fixtureParent, *fixtureChild, *fixtureBar, *fixtureArea, *fixtureWindow, *fixturePage, *fixtureDisclosure;
static NSString *scenario;
static NSUInteger navigation, selections, expansions, pagePresses, pending, ticks;
static NSMutableArray *selectedIDs;
static FixtureNode *Node(NSString *name, NSDictionary *attributes) {
    FixtureNode *node=[FixtureNode new];node.name=name;node.attributes=[attributes mutableCopy];return node;
}
static void Children(FixtureNode *parent, NSArray *children) {
    parent.attributes[@"AXChildren"]=children;
    for (FixtureNode *child in children) child.attributes[@"AXParent"]=parent;
}
static AXError FixtureCopy(AXUIElementRef element, CFStringRef key, CFTypeRef *value) {
    FixtureNode *node=(__bridge FixtureNode *)element;
    id result=node.attributes[(__bridge NSString *)key];
    if (!result) return kAXErrorAttributeUnsupported;
    *value=CFBridgingRetain(result);return kAXErrorSuccess;
}
static AXError FixtureSettable(AXUIElementRef element, CFStringRef key, Boolean *value) {
    *value=(CFEqual(key,kAXDisclosingAttribute) && ![scenario hasPrefix:@"button-expansion"]) || CFEqual(key,kAXValueAttribute);return kAXErrorSuccess;
}
static void Reveal(void) {
    fixtureTable.attributes[@"AXRows"]=@[fixtureParent,fixtureChild];
    fixtureParent.attributes[@"AXDisclosedRows"]=@[fixtureChild];
    fixtureParent.attributes[@"AXDisclosureLevel"]=@0;
    fixtureChild.attributes[@"AXDisclosedByRow"]=fixtureParent;
    fixtureChild.attributes[@"AXDisclosureLevel"]=@1;
}
static AXError FixtureSet(AXUIElementRef element, CFStringRef key, CFTypeRef value) {
    FixtureNode *node=(__bridge FixtureNode *)element;
    if (CFEqual(key,kAXDisclosingAttribute)) {
        if (node!=fixtureParent || !CFEqual(value,kCFBooleanTrue)) return kAXErrorFailure;
        expansions++;navigation++;node.attributes[@"AXDisclosing"]=@YES;
        if ([scenario isEqual:@"moved-expansion"]) fixtureWindow.attributes[@"AXChildren"]=@[];
        else if ([scenario isEqual:@"expanded-parent"]) {
            FixtureNode *label=[node.attributes[@"AXChildren"] firstObject];label.attributes[@"AXValue"]=@"Target";node.attributes[@"AXChildren"]=@[label];
        }
        else Reveal();
    } else if (CFEqual(key,kAXSelectedRowsAttribute)) {
        NSArray *rows=(__bridge NSArray *)value;
        FixtureNode *row=rows.firstObject;
        if (rows.count!=1 || row==fixtureParent) return kAXErrorFailure;
        selections++;[selectedIDs addObject:row.name];node.attributes[@"AXSelectedRows"]=rows;
    } else if (node==fixtureBar && CFEqual(key,kAXValueAttribute)) {
        navigation++;node.attributes[@"AXValue"]=(__bridge id)value;
        if ([scenario isEqual:@"reset-top"]) fixtureTable.attributes[@"AXRows"]=@[fixtureChild];
    } else return kAXErrorFailure;
    return kAXErrorSuccess;
}
static AXError FixtureActions(AXUIElementRef element, CFArrayRef *actions) {
    *actions=(CFArrayRef)CFBridgingRetain(@[@"AXPress"]);return kAXErrorSuccess;
}
static AXError FixturePerform(AXUIElementRef element, CFStringRef action) {
    if ((__bridge FixtureNode *)element==fixtureDisclosure && CFEqual(action,kAXPressAction)) return FixtureSet((__bridge AXUIElementRef)fixtureParent,kAXDisclosingAttribute,kCFBooleanTrue);
    if ((__bridge FixtureNode *)element!=fixturePage || !CFEqual(action,kAXPressAction)) return kAXErrorFailure;
    pagePresses++;navigation++;
    if ([scenario isEqual:@"moved-scroll"]) fixturePage.attributes[@"AXParent"]=fixtureWindow;
    else if ([scenario isEqual:@"recycled-delayed"]) {fixtureBar.attributes[@"AXValue"]=@1;pending=4;}
    else if ([scenario isEqual:@"offscreen"] || [scenario isEqual:@"async-scroll"] || [scenario isEqual:@"recycled-row"]) pending=3;
    return kAXErrorSuccess;
}
#define AXUIElementCopyAttributeValue FixtureCopy
#define AXUIElementIsAttributeSettable FixtureSettable
#define AXUIElementSetAttributeValue FixtureSet
#define AXUIElementCopyActionNames FixtureActions
#define AXUIElementPerformAction FixturePerform
#include MAIL_AX_SOURCE

int main(int argc, char **argv) {
 @autoreleasepool {
    scenario=argc>1 ? @(argv[1]) : @"conversation";selectedIDs=[NSMutableArray array];
    FixtureNode *subject=Node(@"subject",@{@"AXIdentifier":@"Mail.messageList.cell.view.subjectLabel",@"AXValue":@"Target"});
    fixtureChild=Node(@"7",@{});Children(fixtureChild,@[subject]);
    FixtureNode *disclosure=Node(@"disclosure",@{@"AXIdentifier":@"Mail.messageList.cell.view.disclosureButton",@"AXValue":@NO,@"AXEnabled":@YES});fixtureDisclosure=disclosure;
    FixtureNode *parentSubject=Node(@"parent-subject",@{@"AXIdentifier":@"Mail.messageList.cell.view.subjectLabel",@"AXValue":@"Different conversation subject"});
    fixtureParent=Node(@"parent",@{@"AXDisclosing":@NO});Children(fixtureParent,@[parentSubject,disclosure]);
    if ([scenario isEqual:@"button-expansion-numeric"] || [scenario isEqual:@"button-expansion-unknown"]) {
        [fixtureParent.attributes removeObjectForKey:@"AXDisclosing"];
        disclosure.attributes[@"AXValue"]=[scenario isEqual:@"button-expansion-numeric"] ? @0 : @"unknown";
    }
    fixtureTable=Node(@"table",@{@"AXIdentifier":@"Mail.messageList",@"AXRows":@[fixtureParent],@"AXSelectedRows":@[]});
    fixtureBar=Node(@"bar",@{@"AXRole":@"AXScrollBar",@"AXValue":@0,@"AXMinValue":@0,@"AXMaxValue":@1});
    fixturePage=Node(@"page",@{@"AXRole":@"AXButton",@"AXSubrole":@"AXIncrementPage",@"AXEnabled":@YES});Children(fixtureBar,@[fixturePage]);
    fixtureArea=Node(@"area",@{@"AXRole":@"AXScrollArea",@"AXVerticalScrollBar":fixtureBar});Children(fixtureArea,@[fixtureTable,fixtureBar]);
    fixtureWindow=Node(@"window",@{@"AXIdentifier":@"Mail.messageViewer.window.42"});Children(fixtureWindow,@[fixtureArea]);
    FixtureNode *root=Node(@"root",@{@"AXWindows":@[fixtureWindow]});
    MailArchiveAXSession *s=[MailArchiveAXSession new];s.root=root;s.window=fixtureWindow;s.table=fixtureTable;s.windowID=@"Mail.messageViewer.window.42";s.tried=[NSMutableArray array];s.expanded=[NSMutableArray array];
    BOOL offscreen=[scenario isEqual:@"offscreen"] || [scenario isEqual:@"no-progress"] || [scenario isEqual:@"moved-scroll"] || [scenario isEqual:@"async-scroll"] || [scenario isEqual:@"reset-top"] || [scenario isEqual:@"guard-scroll"];
    if (offscreen) fixtureTable.attributes[@"AXRows"]=@[];
    if ([scenario isEqual:@"reset-top"]) fixtureBar.attributes[@"AXValue"]=@0.5;
    if ([scenario hasPrefix:@"recycled-"]) {fixtureChild.name=@"8";fixtureTable.attributes[@"AXRows"]=@[fixtureChild];}
    if ([scenario isEqual:@"leaf-metadata"]) fixtureChild.attributes[@"AXDisclosing"]=@NO;
    if ([scenario isEqual:@"duplicate-subject"]) {
        FixtureNode *other=Node(@"8",@{});Children(other,@[subject]);
        fixtureTable.attributes[@"AXRows"]=@[other,fixtureChild];
    }
    if ([scenario isEqual:@"wrong-child"]) fixtureChild.name=@"8";
    NSString *error=nil;BOOL matched=NO;
    @try {
      for (ticks=0;ticks<50;ticks++) {
        if (pending && --pending==0) {fixtureBar.attributes[@"AXValue"]=@1;fixtureTable.attributes[@"AXRows"]=@[fixtureChild];if ([scenario hasPrefix:@"recycled-"]) fixtureChild.name=@"7";}
        operationDeadline=NSProcessInfo.processInfo.systemUptime+2;
        if (([scenario isEqual:@"guard-expansion"] && [s.pendingAction isEqual:@"expand"]) || ([scenario isEqual:@"guard-scroll"] && [s.pendingAction isEqual:@"page"])) {
            Fail(@"fixture Mail preview/account changed after discovery",2);
        }
        if (SelectStep(s,@"Target")) {
            // Simulate the caller's independent exact message identity check.
            if ([((FixtureNode *)s.selected).name isEqual:@"7"]) {Guard(s);matched=YES;break;}
        }
      }
    } @catch(NSException *exception) {error=exception.reason;}
    NSDictionary *result=@{@"matched":@(matched),@"error":error ?: @"",@"expansions":@(expansions),@"pages":@(pagePresses),@"selections":@(selections),@"selectedIDs":selectedIDs,@"ticks":@(ticks)};
    NSData *encoded=[NSJSONSerialization dataWithJSONObject:result options:0 error:nil];puts([[NSString alloc]initWithData:encoded encoding:NSUTF8StringEncoding].UTF8String);
 }
 return 0;
}
