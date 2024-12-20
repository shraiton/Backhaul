package transport

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// BufferedConn wraps a net.Conn and buffers the first 4KB of data.
type BufferedConn struct {
	net.Conn
	buffer    []byte     // Buffer to store the initial 4KB
	bufferPos int        // Current read position in the buffer
	mu        sync.Mutex // Mutex to protect buffer access
}

// NewBufferedConn initializes a BufferedConn by reading the first 4KB.
func NewBufferedConn(conn net.Conn, bufferSize int) (*BufferedConn, error) {
	bc := &BufferedConn{
		Conn:      conn,
		buffer:    make([]byte, 0, bufferSize),
		bufferPos: 0,
	}

	// Temporary buffer to read initial data
	tempBuffer := make([]byte, bufferSize)
	conn.SetReadDeadline(time.Now().Add(time.Duration(3 * time.Second)))
	n, err := conn.Read(tempBuffer)
	if err != nil {
		if err == io.EOF {
			// Connection closed by client before sending data
			return nil, fmt.Errorf("Connection closed by client before sending data: %w", err)
			//return bc, nil
		}
		return nil, fmt.Errorf("error reading initial data: %w", err)
	}
	conn.SetReadDeadline(time.Time{})

	// Store the read data in the buffer
	bc.buffer = append(bc.buffer, tempBuffer[:n]...)
	return bc, nil
}

// Read overrides the default Read method to first serve data from the buffer.
func (bc *BufferedConn) Read(p []byte) (int, error) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	// If there is data in the buffer, read from it first
	if bc.bufferPos < len(bc.buffer) {
		n := copy(p, bc.buffer[bc.bufferPos:])
		bc.bufferPos += n
		return n, nil
	}

	// Buffer has been fully read; delegate to the underlying connection
	return bc.Conn.Read(p)
}
