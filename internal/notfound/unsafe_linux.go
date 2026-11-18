//go:build linux

package notfound

import "unsafe"

func unsafePointer(b *byte) unsafe.Pointer { return unsafe.Pointer(b) }
