// Package status serves and reads heatctl's local status socket. The socket is
// the only local endpoint heatctl exposes (C2); it is not state (C7).
package status

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/enrico/heatctl/internal/control"
)

// DefaultSocketPath returns the socket the run and status operations use when
// no path is given. HEATCTL_SOCKET overrides it.
func DefaultSocketPath() string {
	if p := os.Getenv("HEATCTL_SOCKET"); p != "" {
		return p
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return filepath.Join(dir, "heatctl.sock")
		}
	}
	uid := os.Getuid()
	runUser := filepath.Join("/run/user", strconv.Itoa(uid))
	if st, err := os.Stat(runUser); err == nil && st.IsDir() {
		return filepath.Join(runUser, "heatctl.sock")
	}
	if uid == 0 {
		return "/run/heatctl/heatctl.sock"
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("heatctl-%d", uid), "heatctl.sock")
}

// ErrAlreadyRunning reports that another controller is listening on the
// socket.
var ErrAlreadyRunning = errors.New("another heatctl is already running")

// Server serves status requests on a unix socket.
type Server struct {
	path string
	ln   net.Listener
}

// Listen creates the socket. The parent directory is created when needed.
// Only the socket file is ever written there.
func Listen(path string) (*Server, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		// A stale socket from a crash would block Listen; a live one means a
		// controller is already running.
		if conn, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%w (socket %s)", ErrAlreadyRunning, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return &Server{path: path, ln: ln}, nil
}

// Serve answers status requests until Close is called.
func (s *Server) Serve(snapshot func() *control.Snapshot) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn, snapshot)
	}
}

type request struct {
	Op string `json:"op"`
}

type response struct {
	Schema   int               `json:"schema"`
	Snapshot *control.Snapshot `json:"snapshot,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func (s *Server) handle(conn net.Conn, snapshot func() *control.Snapshot) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(io.LimitReader(conn, 4096))
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(conn, response{Schema: 1, Error: "invalid request"})
		return
	}
	if req.Op != "status" {
		writeResponse(conn, response{Schema: 1, Error: "unsupported operation " + req.Op})
		return
	}
	writeResponse(conn, response{Schema: 1, Snapshot: snapshot()})
}

func writeResponse(conn net.Conn, resp response) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

// Close stops serving and removes the socket file.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	err := s.ln.Close()
	_ = os.Remove(s.path)
	return err
}

// Fetch asks the controller listening on path for its status. It returns a
// wrapped error when no controller is listening.
func Fetch(path string, timeout time.Duration) (*control.Snapshot, error) {
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return nil, fmt.Errorf("no heatctl controller is running at %s: %w", path, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(`{"op":"status"}` + "\n")); err != nil {
		return nil, fmt.Errorf("no heatctl controller is running at %s: %w", path, err)
	}
	r := bufio.NewReader(io.LimitReader(conn, 4<<20))
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, fmt.Errorf("no heatctl controller is running at %s: %w", path, err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("invalid response from %s: %w", path, err)
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	if resp.Snapshot == nil {
		return nil, errors.New("controller returned an empty status")
	}
	return resp.Snapshot, nil
}
