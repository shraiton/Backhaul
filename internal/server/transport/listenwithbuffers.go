package transport

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"syscall"
	"time"
)

// listenWithBuffers creates a TCP listener with specified SO_RCVBUF and SO_SNDBUF sizes.
// It returns a net.Listener and an error if any.
func listenWithBuffers(network, address string, rcvBufSize, sndBufSize int, mss int, congestionControl string) (net.Listener, error) {
	// Options
	listenerCfg := &net.ListenConfig{
		Control: func(network, address string, s syscall.RawConn) error {
			err := ReusePortControl(network, address, s)
			if err != nil {
				return err
			}

			if rcvBufSize > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, rcvBufSize); err != nil {
						err = fmt.Errorf("failed to set SO_RCVBUF: %v", err)
					}
				})
			}
			if err != nil {
				return err
			}

			if sndBufSize > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, sndBufSize); err != nil {
						err = fmt.Errorf("failed to set SO_SNDBUF: %v", err)
					}
				})
			}

			if mss > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, mss); err != nil {
						err = fmt.Errorf("failed to set MSS: %v", err)
					}
				})
			}

			if len(congestionControl) > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptString(int(fd), syscall.IPPROTO_TCP, syscall.TCP_CONGESTION, congestionControl); err != nil {
						err = fmt.Errorf("failed to set Congestion Control: %v", err)
					}
				})
			}

			err = s.Control(func(fd uintptr) {
				if err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1); err != nil {
					err = fmt.Errorf("failed to set TCP NoDelay: %v", err)
				}
			})

			return err

		},
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Interval: 10 * time.Second, Count: 5, Idle: 30},
	}

	// Dial the TCP connection with a timeout
	ctx, _ := context.WithTimeout(context.Background(), 3*time.Second)
	listener, err := listenerCfg.Listen(ctx, network, address)
	if err != nil {
		return nil, err
	}

	return listener, err

}

func ReusePortControl(network, address string, s syscall.RawConn) error {
	var controlErr error

	// Set socket options
	err := s.Control(func(fd uintptr) {
		// Set SO_REUSEADDR
		if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
			controlErr = fmt.Errorf("failed to set SO_REUSEADDR: %v", err)
			return
		}

		// Conditionally set SO_REUSEPORT only on Linux
		if runtime.GOOS == "linux" {
			if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, 0xf /* SO_REUSEPORT */, 1); err != nil {
				controlErr = fmt.Errorf("failed to set SO_REUSEPORT: %v", err)
				return
			}
		}
	})

	if err != nil {
		return err
	}

	return controlErr
}
