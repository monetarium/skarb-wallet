// SPDX-License-Identifier: Unlicense OR MIT

// Skarb: android is NOT excluded here, unlike the *_abi.c files.
//
// The precompiled CPU shaders ship as *_linux_<arch>.syso, and a file name
// carrying a GOOS is the only constraint a .syso can have — there is no way
// to write "not android" on one. The go tool deliberately accepts _linux
// files when building for android, so those objects get linked into the APK
// even though the compute path is dead there (cpu.Supported is false). They
// call coroutine_alloc_frame/coroutine_free_frame, so those two must exist
// or the dynamic loader refuses libgio.so and the app dies on the first
// frame with UnsatisfiedLinkError.
//go:build linux && (arm64 || arm || amd64)

#include <stdint.h>
#include <stdlib.h>
#include <assert.h>
#include "abi.h"
#include "runtime.h"

static void *malloc_align(size_t alignment, size_t size) {
	void *ptr;
	int ret = posix_memalign(&ptr, alignment, size);
	assert(ret == 0);
	return ptr;
}

ATTR_HIDDEN void *coroutine_alloc_frame(size_t size) {
	void *ptr = malloc_align(16, size);
	return ptr;
}

ATTR_HIDDEN void coroutine_free_frame(void *ptr) {
	free(ptr);
}
