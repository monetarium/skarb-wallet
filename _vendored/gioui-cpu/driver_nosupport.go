// SPDX-License-Identifier: Unlicense OR MIT

// Skarb: android added to the no-support set. The cgo variant's aliases to
// C types stopped compiling with Go ≥1.25 cgo ("cannot define new methods on
// non-local type" — over-aligned/incomplete structs map to cgo.Incomplete),
// and the CPU compute fallback is pointless on Android anyway: every arm64
// device renders through the GLES driver. Supported=false makes Gio's gpu
// package skip the compute path entirely.
//go:build !(linux && (arm64 || arm || amd64)) || android

package cpu

import "unsafe"

type (
	BufferDescriptor  struct{}
	ImageDescriptor   struct{}
	SamplerDescriptor struct{}

	DispatchContext struct{}
	ThreadContext   struct{}
	ProgramInfo     struct{}
)

const Supported = false

func NewBuffer(size int) BufferDescriptor {
	panic("unsupported")
}

func (d *BufferDescriptor) Data() []byte {
	panic("unsupported")
}

func (d *BufferDescriptor) Free() {
}

func NewImageRGBA(width, height int) ImageDescriptor {
	panic("unsupported")
}

func (d *ImageDescriptor) Data() []byte {
	panic("unsupported")
}

func (d *ImageDescriptor) Free() {
}

func NewDispatchContext() *DispatchContext {
	panic("unsupported")
}

func (c *DispatchContext) Free() {
}

func (c *DispatchContext) Prepare(numThreads int, prog *ProgramInfo, descSet unsafe.Pointer, x, y, z int) {
	panic("unsupported")
}

func (c *DispatchContext) Dispatch(threadIdx int, ctx *ThreadContext) {
	panic("unsupported")
}

func NewThreadContext() *ThreadContext {
	panic("unsupported")
}

func (c *ThreadContext) Free() {
}
