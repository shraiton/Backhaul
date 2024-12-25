package utils

import (
	"sync"
)

const BufferSize = 32 * 1024 // 32 KB buffer size

// BufferPool is a pool of reusable buffers
var BufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, BufferSize)
		return &buf
	},
}

// GetBuffer retrieves a pointer to a buffer from the pool
func GetBuffer() *[]byte {
	return BufferPool.Get().(*[]byte)
}

// PutBuffer returns a buffer to the pool
func PutBuffer(buffer *[]byte) {
	BufferPool.Put(buffer)
}

const BufferSize4k = 4 * 1024 // 32 KB buffer size
// BufferPool is a pool of reusable buffers
var BufferPool4k = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, BufferSize4k)
		return &buf
	},
}

// GetBuffer retrieves a pointer to a buffer from the pool
func GetBuffer4k() *[]byte {
	return BufferPool4k.Get().(*[]byte)
}

// PutBuffer returns a buffer to the pool
func PutBuffer4k(buffer *[]byte) {
	*buffer = (*buffer)[:0] // Reset length only
	BufferPool4k.Put(buffer)
}
