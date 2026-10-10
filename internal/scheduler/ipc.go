package scheduler

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// The CLI talks to the scheduler over a Unix socket: one JSON request line,
// one JSON response line, then the connection is closed.

type request struct {
	Op string `json:"op"`           // one of the op constants
	ID int64  `json:"id,omitempty"` // the job, for opKill
}

const (
	opPing = "ping" // answer with Info and nothing else
	opWake = "wake" // jobs were added or cancelled: look at the schedule again
	opKill = "kill" // stop a running job
)

// ioTimeout bounds one exchange, on either side of the socket.
const ioTimeout = 2 * time.Second

// response is Info plus the outcome of the operation. Schedulers from before
// "kill" existed answer every request with a bare Info.
type response struct {
	Info
	Killed bool   `json:"killed,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Info describes a running scheduler.
type Info struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
}

// ErrUnavailable is returned when no scheduler answers on the socket.
var ErrUnavailable = errors.New("background scheduler is unavailable")

func call(socket string, req request) (response, error) {
	conn, err := net.DialTimeout("unix", socket, ioTimeout)
	if err != nil {
		return response{}, ErrUnavailable
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(ioTimeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, ErrUnavailable
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return response{}, ErrUnavailable
	}
	return resp, nil
}

// Ping checks that a scheduler is alive.
func Ping(socket string) (Info, error) {
	resp, err := call(socket, request{Op: opPing})
	return resp.Info, err
}

// Wake tells the scheduler that jobs were added or cancelled, so that it
// recomputes when to wake up next.
func Wake(socket string) error {
	_, err := call(socket, request{Op: opWake})
	return err
}

// Kill asks the scheduler to stop a running job. It returns once the job has
// been signalled, not once it has exited.
func Kill(socket string, id int64) error {
	resp, err := call(socket, request{Op: opKill, ID: id})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if !resp.Killed {
		return fmt.Errorf("the running scheduler (version %s) is too old to stop jobs; restart it first (see `laterr doctor`)", resp.Version)
	}
	return nil
}

// listen opens the scheduler's socket, readable by the user only. The caller
// holds the scheduler lock, which guarantees no other scheduler owns this
// socket, so a leftover file is stale.
func listen(socket string) (net.Listener, error) {
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func (s *scheduler) serve(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handle(conn)
	}
}

func (s *scheduler) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(ioTimeout))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var req request
	if json.Unmarshal(line, &req) != nil {
		return
	}
	resp := response{Info: Info{Version: s.version, PID: os.Getpid()}}
	switch req.Op {
	case opWake:
		s.notify()
	case opKill:
		if err := s.kill(req.ID); err != nil {
			resp.Error = err.Error()
		} else {
			resp.Killed = true
		}
	}
	json.NewEncoder(conn).Encode(resp)
}
