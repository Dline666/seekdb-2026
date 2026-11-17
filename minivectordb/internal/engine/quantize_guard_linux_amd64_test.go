package engine

import (
	"bytes"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

func TestQuantizeEncodingGuardPages(t *testing.T) {
	if !useSIMD {
		t.Skip("AVX2 unavailable or explicitly disabled")
	}
	page := syscall.Getpagesize()
	old := quantizeEncodingEnabled
	defer func() { quantizeEncodingEnabled = old }()
	for _, n := range []int{16, 17, 31, 127, 128, 1024} {
		func() {
			input, err := syscall.Mmap(-1, 0, 2*page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Munmap(input)
			output, err := syscall.Mmap(-1, 0, 2*page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Munmap(output)
			if err = syscall.Mprotect(input[page:], syscall.PROT_NONE); err != nil {
				t.Fatal(err)
			}
			if err = syscall.Mprotect(output[page:], syscall.PROT_NONE); err != nil {
				t.Fatal(err)
			}
			v := unsafe.Slice((*float32)(unsafe.Pointer(&input[page-4*n])), n)
			codes := output[page-n : page : page]
			for i := range v {
				v[i] = float32(i%257) - 128
			}
			want := make([]byte, n)
			quantizeEncodingEnabled = false
			quantizeRow(v, want)
			quantizeEncodingEnabled = true
			quantizeRow(v, codes)
			if !bytes.Equal(codes, want) {
				t.Fatal("guard-page encoding mismatch", n)
			}
			runtime.KeepAlive(v)
			runtime.KeepAlive(codes)
		}()
	}
}
