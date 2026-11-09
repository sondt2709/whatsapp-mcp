//go:build darwin

package keystore

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// keychain stores the key as a generic password in the login Keychain. It
// calls Security.framework directly (purego, no CGO). An item that a program
// adds this way trusts only that program, so another program gets a macOS
// dialog. /usr/bin/security is not used: an item that it adds trusts
// /usr/bin/security, and then any program can read it with no dialog.
type keychain struct{}

func system() Store { return keychain{} }

// Name implements Store.
func (keychain) Name() string { return "macOS Keychain" }

// Security.framework status codes.
const (
	errSecSuccess              = 0
	errSecUserCanceled         = -128
	errSecAuthFailed           = -25293
	errSecDuplicateItem        = -25299
	errSecItemNotFound         = -25300
	errSecInteractionNotAllowd = -25308
)

const cfUTF8 = 0x08000100

var (
	loadOnce sync.Once
	loadErr  error
	cf, sec  uintptr

	cfStringCreate     func(alloc uintptr, s string, enc uint32) uintptr
	cfDataCreate       func(alloc uintptr, b *byte, n int) uintptr
	cfDataGetLength    func(d uintptr) int
	cfDataGetBytePtr   func(d uintptr) *byte
	cfDictionaryCreate func(alloc uintptr, keys, vals *uintptr, n int, kcb, vcb uintptr) uintptr
	cfRelease          func(p uintptr)
	secItemAdd         func(attrs uintptr, result *uintptr) int32
	secItemCopyMatch   func(query uintptr, result *uintptr) int32
	secItemDelete      func(query uintptr) int32
)

func load() error {
	loadOnce.Do(func() {
		var err error
		if cf, err = purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			loadErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		if sec, err = purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			loadErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		purego.RegisterLibFunc(&cfStringCreate, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&cfDataCreate, cf, "CFDataCreate")
		purego.RegisterLibFunc(&cfDataGetLength, cf, "CFDataGetLength")
		purego.RegisterLibFunc(&cfDataGetBytePtr, cf, "CFDataGetBytePtr")
		purego.RegisterLibFunc(&cfDictionaryCreate, cf, "CFDictionaryCreate")
		purego.RegisterLibFunc(&cfRelease, cf, "CFRelease")
		purego.RegisterLibFunc(&secItemAdd, sec, "SecItemAdd")
		purego.RegisterLibFunc(&secItemCopyMatch, sec, "SecItemCopyMatching")
		purego.RegisterLibFunc(&secItemDelete, sec, "SecItemDelete")
	})
	return loadErr
}

// constant returns the value of a global CFTypeRef constant of a framework.
func constant(lib uintptr, name string) uintptr {
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		panic("keystore: missing symbol " + name)
	}
	// p is the address of the constant; read the CFTypeRef that it holds.
	return **(**uintptr)(unsafe.Pointer(&p))
}

func symbol(lib uintptr, name string) uintptr {
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		panic("keystore: missing symbol " + name)
	}
	return p
}

// query holds the CF objects of one dictionary and releases them together.
type query struct {
	owned []uintptr
	keys  []uintptr
	vals  []uintptr
}

func (q *query) str(key uintptr, s string) {
	v := cfStringCreate(0, s, cfUTF8)
	q.owned = append(q.owned, v)
	q.keys, q.vals = append(q.keys, key), append(q.vals, v)
}

func (q *query) data(key uintptr, b []byte) {
	v := cfDataCreate(0, &b[0], len(b))
	q.owned = append(q.owned, v)
	q.keys, q.vals = append(q.keys, key), append(q.vals, v)
}

func (q *query) raw(key, val uintptr) {
	q.keys, q.vals = append(q.keys, key), append(q.vals, val)
}

func (q *query) dict() uintptr {
	d := cfDictionaryCreate(0, &q.keys[0], &q.vals[0], len(q.keys),
		symbol(cf, "kCFTypeDictionaryKeyCallBacks"), symbol(cf, "kCFTypeDictionaryValueCallBacks"))
	q.owned = append(q.owned, d)
	return d
}

func (q *query) release() {
	for _, p := range q.owned {
		cfRelease(p)
	}
}

func item(account string) *query {
	q := &query{}
	q.raw(constant(sec, "kSecClass"), constant(sec, "kSecClassGenericPassword"))
	q.str(constant(sec, "kSecAttrService"), Service)
	q.str(constant(sec, "kSecAttrAccount"), account)
	return q
}

func status(op string, st int32) error {
	switch st {
	case errSecSuccess:
		return nil
	case errSecItemNotFound:
		return ErrNotFound
	case errSecUserCanceled, errSecAuthFailed, errSecInteractionNotAllowd:
		return fmt.Errorf("%w: %s status %d", ErrDenied, op, st)
	}
	return fmt.Errorf("keychain %s: status %d", op, st)
}

// Get implements Store.
func (keychain) Get(account string) ([]byte, error) {
	if err := load(); err != nil {
		return nil, err
	}
	q := item(account)
	defer q.release()
	q.raw(constant(sec, "kSecReturnData"), constant(cf, "kCFBooleanTrue"))
	q.raw(constant(sec, "kSecMatchLimit"), constant(sec, "kSecMatchLimitOne"))
	var out uintptr
	if err := status("read", secItemCopyMatch(q.dict(), &out)); err != nil {
		return nil, err
	}
	defer cfRelease(out)
	n := cfDataGetLength(out)
	b := unsafe.Slice(cfDataGetBytePtr(out), n)
	return append([]byte(nil), b...), nil
}

// Set implements Store. It replaces an existing key.
func (k keychain) Set(account string, key []byte) error {
	if err := load(); err != nil {
		return err
	}
	if len(key) == 0 {
		return fmt.Errorf("keychain: empty key")
	}
	if err := k.Delete(account); err != nil {
		return err
	}
	q := item(account)
	defer q.release()
	q.str(constant(sec, "kSecAttrLabel"), Service)
	q.data(constant(sec, "kSecValueData"), key)
	return status("add", secItemAdd(q.dict(), nil))
}

// Delete implements Store. A missing item is not an error.
func (keychain) Delete(account string) error {
	if err := load(); err != nil {
		return err
	}
	q := item(account)
	defer q.release()
	err := status("delete", secItemDelete(q.dict()))
	if err == ErrNotFound {
		return nil
	}
	return err
}
