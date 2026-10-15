//go:build darwin && cgo

package gmail

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

static CFMutableDictionaryRef gmail_query(const char *account) {
    CFStringRef value = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
    if (!value) return NULL;
    CFMutableDictionaryRef query = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    if (!query) { CFRelease(value); return NULL; }
    CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(query, kSecAttrService, CFSTR("mail-app-cli.gmail"));
    CFDictionarySetValue(query, kSecAttrAccount, value);
    CFRelease(value);
    return query;
}
static int gmail_load(const char *account, void **out, long *length) {
    CFMutableDictionaryRef query = gmail_query(account);
    if (!query) return errSecAllocate;
    CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
    CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching(query, &result);
    CFRelease(query);
    if (status != errSecSuccess) return status;
    if (!result || CFGetTypeID(result) != CFDataGetTypeID()) { if (result) CFRelease(result); return errSecDecode; }
    CFIndex size = CFDataGetLength((CFDataRef) result);
    if (size < 1 || size > 65536) { CFRelease(result); return errSecDecode; }
    *out = malloc(size);
    if (!*out) { CFRelease(result); return errSecAllocate; }
    memcpy(*out, CFDataGetBytePtr((CFDataRef)result), size);
    *length = size;
    CFRelease(result);
    return errSecSuccess;
}
static int gmail_store(const char *account, const void *bytes, long length) {
    CFMutableDictionaryRef query = gmail_query(account);
    if (!query) return errSecAllocate;
    CFDataRef data = CFDataCreate(NULL, bytes, length);
    if (!data) { CFRelease(query); return errSecAllocate; }
    const void *keys[] = { kSecValueData };
    const void *values[] = { data };
    CFDictionaryRef attributes = CFDictionaryCreate(NULL, keys, values, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    if (!attributes) { CFRelease(query); CFRelease(data); return errSecAllocate; }
    OSStatus status = SecItemUpdate(query, attributes);
    if (status == errSecItemNotFound) {
        CFDictionarySetValue(query, kSecValueData, data);
        CFDictionarySetValue(query, kSecAttrAccessible, kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly);
        status = SecItemAdd(query, NULL);
    }
    CFRelease(attributes); CFRelease(data); CFRelease(query);
    return status;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func KeychainAvailable() bool { return true }
func keychainLoad(account string) ([]byte, error) {
	name := C.CString(account)
	defer C.free(unsafe.Pointer(name))
	var data unsafe.Pointer
	var size C.long
	status := C.gmail_load(name, &data, &size)
	if status == C.errSecItemNotFound {
		return nil, ErrCredentialsNotFound
	}
	if status != 0 {
		return nil, fmt.Errorf("Gmail Keychain read failed (Security status %d)", int(status))
	}
	defer C.free(data)
	return C.GoBytes(data, C.int(size)), nil
}
func keychainStore(account string, data []byte) error {
	if len(data) == 0 || len(data) > 65536 {
		return fmt.Errorf("Gmail Keychain credential size is invalid")
	}
	name := C.CString(account)
	defer C.free(unsafe.Pointer(name))
	bytes := C.CBytes(data)
	defer C.free(bytes)
	status := C.gmail_store(name, bytes, C.long(len(data)))
	if status != 0 {
		return fmt.Errorf("Gmail Keychain write failed (Security status %d)", int(status))
	}
	return nil
}
