// Package statussock serves the controller's status on a local Unix socket.
//
// The protocol is minimal: a client connects and the server writes one JSON
// document and closes the connection. The socket is the only file heatctl
// creates.
package statussock

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// DefaultPath is where the socket lives unless configured otherwise.
const DefaultPath = "/run/heatctl/heatctl.sock"

// ErrNotRunning means no controller answers on the socket.
var ErrNotRunning = errors.New("no heatctl controller is running")

// Server serves status snapshots.
type Server struct {
	ln net.Listener
}

// Listen creates the socket at path. A leftover socket from a controller
// that is no longer running is replaced; a live one is an error.
func Listen(path string, snapshot func() []byte) (*Server, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("another heatctl is already serving status on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// Let members of the service's group read the status.
	_ = os.Chmod(path, 0o660)
	s := &Server{ln: ln}
	go s.serve(snapshot)
	return s, nil
}

func (s *Server) serve(snapshot func() []byte) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go func() {
			defer conn.Close()
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, _ = conn.Write(snapshot())
		}()
	}
}

// Close stops serving and removes the socket.
func (s *Server) Close() error { return s.ln.Close() }

// Query fetches the status document from the socket at path.
func Query(path string) ([]byte, error) {
	conn, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w (cannot connect to %s: %v)", ErrNotRunning, path, unwrapSyscall(err))
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	data, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("read status from %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w (empty answer on %s)", ErrNotRunning, path)
	}
	return data, nil
}

func unwrapSyscall(err error) error {
	var op *net.OpError
	if errors.As(err, &op) {
		var se *os.SyscallError
		if errors.As(op.Err, &se) {
			return se.Err
		}
		return op.Err
	}
	return err
}
