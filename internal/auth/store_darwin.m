//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <LocalAuthentication/LocalAuthentication.h>
#import <Security/Security.h>
#include <stdlib.h>
#include <string.h>

static NSMutableDictionary *query(const char *profile) {
    LAContext *context = [[[LAContext alloc] init] autorelease];
    context.interactionNotAllowed = YES;
    return [NSMutableDictionary dictionaryWithDictionary:@{
        (id)kSecClass: (id)kSecClassGenericPassword,
        (id)kSecAttrService: @"tw.org.alive.hhc.cli",
        (id)kSecAttrAccount: [NSString stringWithUTF8String:profile],
        (id)kSecUseAuthenticationContext: context
    }];
}

int hhc_keychain_read(const char *profile, void **bytes, int *size) {
    @autoreleasepool {
        NSMutableDictionary *q = query(profile);
        q[(id)kSecReturnData] = @YES;
        q[(id)kSecMatchLimit] = (id)kSecMatchLimitOne;
        CFTypeRef result = NULL;
        OSStatus status = SecItemCopyMatching((CFDictionaryRef)q, &result);
        if (status != errSecSuccess) return status;
        if (!result || CFGetTypeID(result) != CFDataGetTypeID()) {
            if (result) CFRelease(result);
            return errSecDecode;
        }
        CFIndex length = CFDataGetLength((CFDataRef)result);
        if (length < 1 || length > 2560) { CFRelease(result); return errSecDecode; }
        *bytes = malloc(length);
        if (!*bytes) { CFRelease(result); return errSecAllocate; }
        memcpy(*bytes, CFDataGetBytePtr((CFDataRef)result), length);
        *size = (int)length;
        CFRelease(result);
        return errSecSuccess;
    }
}

int hhc_keychain_save(const char *profile, const void *bytes, int size) {
    @autoreleasepool {
        NSMutableDictionary *q = query(profile);
        NSDictionary *value = @{(id)kSecValueData: [NSData dataWithBytes:bytes length:size]};
        OSStatus status = SecItemUpdate((CFDictionaryRef)q, (CFDictionaryRef)value);
        if (status != errSecItemNotFound) return status;
        [q addEntriesFromDictionary:value];
        return SecItemAdd((CFDictionaryRef)q, NULL);
    }
}

int hhc_keychain_delete(const char *profile) {
    @autoreleasepool { return SecItemDelete((CFDictionaryRef)query(profile)); }
}

void hhc_keychain_free(void *bytes, int size) {
    volatile unsigned char *p = bytes;
    for (int i = 0; i < size; i++) p[i] = 0;
    free(bytes);
}
