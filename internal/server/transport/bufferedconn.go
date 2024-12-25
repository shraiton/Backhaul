package transport

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/musix/backhaul/internal/utils"
)

// BufferedConn wraps a net.Conn and buffers the first 4KB of data.
type BufferedConn struct {
	net.Conn
	buffer    *[]byte    // Buffer to store the initial 4KB
	bufferPos int        // Current read position in the buffer
	mu        sync.Mutex // Mutex to protect buffer access
	closed    bool
}

// NewBufferedConn initializes a BufferedConn by reading the first 4KB.
func NewBufferedConn(conn net.Conn) (*BufferedConn, error) {

	//buffer := buferpool4k.GetBuffer()
	buffer := utils.GetBuffer4k()

	bc := &BufferedConn{
		Conn:      conn,
		buffer:    buffer,
		bufferPos: 0,
		closed:    false,
	}

	conn.SetReadDeadline(time.Now().Add(time.Duration(3 * time.Second)))
	n, err := conn.Read((*buffer)[:cap(*buffer)])
	if err != nil {
		utils.PutBuffer4k(buffer)
		if err == io.EOF {
			// Connection closed by client before sending data
			return nil, fmt.Errorf("Connection closed by client before sending data: %w", err)
			//return bc, nil
		}
		return nil, fmt.Errorf("error reading initial data: %w", err)
	}
	conn.SetReadDeadline(time.Time{})

	// Store the read data in the buffer
	*buffer = (*buffer)[:n]
	return bc, nil
}

// Read overrides the default Read method to first serve data from the buffer.
func (bc *BufferedConn) Read(p []byte) (int, error) {

	bc.mu.Lock()
	defer bc.mu.Unlock()
	if bc.buffer != nil && bc.closed == false && bc.bufferPos < len(*bc.buffer) { // if it was closed don't come here at all
		n := copy(p, (*bc.buffer)[bc.bufferPos:])
		bc.bufferPos += n

		if bc.buffer != nil && bc.bufferPos == len(*bc.buffer) {
			utils.PutBuffer4k(bc.buffer) // Return buffer to the pool
			bc.buffer = nil
		}

		return n, nil
	}
	//the first part will return and unlocks the mu for sure then when we return the bc.Conn.Read the mu is already unlocked

	// Buffer has been fully read; delegate to the underlying connection
	return bc.Conn.Read(p)
}

func (bc *BufferedConn) Close() error {
	bc.mu.Lock()
	if bc.buffer != nil {
		utils.PutBuffer4k(bc.buffer) // Return buffer to the pool
		bc.buffer = nil
	}
	bc.closed = true
	bc.mu.Unlock()

	return bc.Conn.Close()
}
