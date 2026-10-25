package scheduler

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCallBoundsResponses(t *testing.T) {
	for _, size := range []int{32, maxMessageSize + 1} {
		t.Run(fmt.Sprintf("version-bytes-%d", size), func(t *testing.T) {
			// macOS Unix socket addresses must fit in about 100 bytes.
			dir, err := os.MkdirTemp("/tmp", "laterr-ipc-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "test.sock")
			l, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			done := make(chan struct{})
			version := strings.Repeat("v", size)
			go func() {
				defer close(done)
				conn, err := l.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(ioTimeout))
				if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
					return
				}
				json.NewEncoder(conn).Encode(response{Info: Info{Version: version}})
			}()
			info, err := Ping(socket)
			if size < maxMessageSize && (err != nil || info.Version != version) {
				t.Errorf("valid response: %+v, %v", info, err)
			}
			if size > maxMessageSize && !errors.Is(err, ErrUnavailable) {
				t.Errorf("oversized response: %+v, %v", info, err)
			}
			<-done
		})
	}
}

func TestHandleBoundsRequests(t *testing.T) {
	for _, tc := range []struct {
		name, request string
		wantResponse  bool
	}{
		{"ping", "{\"op\":\"ping\"}\n", true},
		{"maximum", "{\"op\":\"ping\"}" + strings.Repeat(" ", maxMessageSize-14) + "\n", true},
		{"oversize", "{\"op\":\"ping\"}" + strings.Repeat(" ", maxMessageSize) + "\n", false},
		{"unterminated", strings.Repeat("x", maxMessageSize+1), false},
		{"malformed", "{oops}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			client.SetDeadline(time.Now().Add(3 * time.Second))
			s := &scheduler{version: "test"}
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.handle(server)
			}()
			// An oversized write can fail as soon as the server closes it.
			io.WriteString(client, tc.request)
			var resp response
			err := json.NewDecoder(client).Decode(&resp)
			if tc.wantResponse && (err != nil || resp.Version != "test") {
				t.Errorf("ping response: %+v, %v", resp, err)
			}
			if !tc.wantResponse && err == nil {
				t.Error("accepted an invalid request")
			}
			<-done
		})
	}
}
