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
