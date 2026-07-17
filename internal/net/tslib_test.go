package net

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"testing"
	"time"
)

// localhostIP returns the uint32 value used as TupleSpaceClient.HostIP in tests
// (0x7F000001 = 127.0.0.1). It is a fixed placeholder rather than a general
// IP-to-uint32 conversion, since tests always run against a local mock server and
// TsPut/TsGet/TsRead only echo HostIP back into the wire structs without interpreting it.
func localhostIP() uint32 {
	return 0x7F000001
}

// MockTshServer is a minimal stand-in for a real TSH daemon, used by the tests in this
// file to script specific server-side response sequences (immediate vs. delayed
// responses, UVR callbacks, etc.) against the TupleSpaceClient methods under test.
type MockTshServer struct {
	Listener net.Listener
	Addr     string
	// Queues for expected behaviors or channels to signal test progress
	putChan chan []byte
	getChan chan string
	// doneChan is used by each test's server goroutine to report completion (nil) or
	// a mismatch/protocol error (non-nil) back to the test's main goroutine.
	doneChan chan error
}

// startMockServer binds MockTshServer's Listener to an OS-assigned port on
// 127.0.0.1, failing the test immediately (t.Fatalf) if binding fails.
func startMockServer(t *testing.T) *MockTshServer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start mock server: %v", err)
	}

	return &MockTshServer{
		Listener: ln,
		Addr:     ln.Addr().String(),
		putChan:  make(chan []byte, 1),
		getChan:  make(chan string, 1),
		doneChan: make(chan error, 1),
	}
}

// Close shuts down the mock server's listener.
func (s *MockTshServer) Close() {
	s.Listener.Close()
}

// handlePut is an unused stub (empty body, no callers in this file) left over from an
// earlier/alternate test structure; each TestTsPut_* test currently implements its own
// inline server goroutine instead of dispatching through this method.
func (s *MockTshServer) handlePut(conn net.Conn) {
	// 1. Read OpCode (already read by dispatcher? No, dispatcher peeks or reads)
	// Dispatcher logic below
}

// Manually read/write structs matching tslib.go logic

// readTshPutIt reads a TshPutIt from r field-by-field (mirroring writeTshPutIt's
// encoding, including padding), for use by mock server goroutines that need to parse a
// request written by TupleSpaceClient.TsPut. Returns the decoded struct, or a non-nil
// error if any field read fails.
func readTshPutIt(r io.Reader) (*TshPutIt, error) {
	var s TshPutIt
	if err := binary.Read(r, binary.BigEndian, &s.AppId); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.Name); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.Priority); err != nil {
		return nil, err
	}
	io.ReadFull(r, make([]byte, 2)) // Padding
	if err := binary.Read(r, binary.BigEndian, &s.Host); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.Port); err != nil {
		return nil, err
	}
	io.ReadFull(r, make([]byte, 2)) // Padding
	if err := binary.Read(r, binary.BigEndian, &s.Length); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.ProcId); err != nil {
		return nil, err
	}
	return &s, nil
}

// readTshGetIt reads a TshGetIt from r field-by-field (mirroring writeTshGetIt's
// encoding, including padding), for use by mock server goroutines that need to parse a
// request written by TupleSpaceClient.TsGet/TsRead. Returns the decoded struct, or a
// non-nil error if any field read fails.
func readTshGetIt(r io.Reader) (*TshGetIt, error) {
	var s TshGetIt
	if err := binary.Read(r, binary.BigEndian, &s.AppId); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.Expr); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.Host); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.Port); err != nil {
		return nil, err
	}
	io.ReadFull(r, make([]byte, 2))
	if err := binary.Read(r, binary.BigEndian, &s.Length); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.ProcId); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.CidPort); err != nil {
		return nil, err
	}
	io.ReadFull(r, make([]byte, 2))
	return &s, nil
}

// writeTshGetOt2 writes a TshGetOt2 to w field-by-field in big-endian order with
// explicit padding, for use by mock server goroutines simulating the TSH daemon's
// tuple-details response to TsGet/TsRead. Like the equivalent functions in tslib.go,
// individual binary.Write errors are discarded and the function always returns nil.
func writeTshGetOt2(w io.Writer, s TshGetOt2) error {
	binary.Write(w, binary.BigEndian, s.AppId)
	binary.Write(w, binary.BigEndian, s.Name)
	binary.Write(w, binary.BigEndian, s.Length)
	binary.Write(w, binary.BigEndian, s.Priority)
	binary.Write(w, binary.BigEndian, [2]byte{})
	return nil
}

// TestTsPut_Phase3Storage exercises TupleSpaceClient.TsPut's full three-phase path: a
// mock server returns FAILURE on the initial put attempt (forcing the UVR wait loop),
// then calls back the client's local listener with a UvrReturnStruct carrying
// Status: FAILURE (signaling end-of-traversal, no consuming match), which should drive
// TsPut into phase 3 (TSH_OP_UVRPut3) where the mock server finally responds with
// SUCCESS and a custom Error code. The test asserts TsPut returns that custom code
// (100) with no error, and that the server observed the expected opcodes and tuple
// bytes at each phase.
func TestTsPut_Phase3Storage(t *testing.T) {
	server := startMockServer(t)
	defer server.Close()

	testData := []byte("hello world")
	testName := "greeting"

	// Server Routine
	go func() {
		// Connection 1: Phase 1 Attempt
		conn1, err := server.Listener.Accept()
		if err != nil {
			return
		}
		defer conn1.Close()

		var opCode uint16
		binary.Read(conn1, binary.BigEndian, &opCode)
		if opCode != TSH_OP_PUT {
			server.doneChan <- fmt.Errorf("expected OP_PUT, got %d", opCode)
			return
		}

		putIt, err := readTshPutIt(conn1)
		if err != nil {
			server.doneChan <- err
			return
		}

		// Read Byte data
		buf := make([]byte, putIt.Length)
		io.ReadFull(conn1, buf)
		if !reflect.DeepEqual(buf, testData) {
			server.doneChan <- fmt.Errorf("data mismatch")
			return
		}

		// Return FAILURE to verify Phase 3 logic
		resp := TshPutOt{Status: FAILURE, Error: 0}
		binary.Write(conn1, binary.BigEndian, resp)

		// Wait a bit for client to listen
		time.Sleep(50 * time.Millisecond)

		// Connect back to client for Phase 2 (UVR) - send "No match"
		clientAddr := fmt.Sprintf("127.0.0.1:%d", putIt.Port)
		conn2, err := net.Dial("tcp", clientAddr)
		if err != nil {
			server.doneChan <- fmt.Errorf("failed to callback client: %v", err)
			return
		}

		// Send UvrReturnStruct with FAILURE (End of traversal)
		uvrRet := UvrReturnStruct{Status: FAILURE}
		// Need to write manually because UvrReturnStruct has padding
		binary.Write(conn2, binary.BigEndian, uvrRet.Host)
		binary.Write(conn2, binary.BigEndian, uvrRet.Port)
		binary.Write(conn2, binary.BigEndian, [2]byte{})
		binary.Write(conn2, binary.BigEndian, uvrRet.Request)
		binary.Write(conn2, binary.BigEndian, uvrRet.Status)
		conn2.Close()

		// Connection 3: Phase 3 Storage
		conn3, err := server.Listener.Accept()
		if err != nil {
			server.doneChan <- fmt.Errorf("accept 3 failed: %v", err)
			return
		}
		defer conn3.Close()

		binary.Read(conn3, binary.BigEndian, &opCode)
		if opCode != TSH_OP_UVRPut3 {
			server.doneChan <- fmt.Errorf("expected OP_UVRPut3, got %d", opCode)
			return
		}
		// Read header and body again
		readTshPutIt(conn3)
		io.ReadFull(conn3, make([]byte, len(testData)))

		// success
		finalResp := TshPutOt{Status: SUCCESS, Error: 100} // Custom code
		binary.Write(conn3, binary.BigEndian, finalResp)

		server.doneChan <- nil
	}()

	client := &TupleSpaceClient{
		TshAddr: server.Addr,
		HostIP:  localhostIP(),
		AppId:   "testApp",
	}

	ret, err := client.TsPut(testName, testData)
	if err != nil {
		t.Fatalf("TsPut failed: %v", err)
	}

	// Wait for server done
	if serverErr := <-server.doneChan; serverErr != nil {
		t.Fatalf("Server error: %v", serverErr)
	}

	if ret != 100 {
		t.Errorf("Expected return 100, got %d", ret)
	}
}

// TestTsGet_Immediate verifies TupleSpaceClient.TsGet's "immediate" path: the mock
// server responds to TSH_OP_GET with TshGetOt1{Status: SUCCESS} on the same
// connection, followed directly by the tuple details and data, and TsGet is expected
// to read the data from that same connection (never touching its callback listener)
// and return it unchanged.
func TestTsGet_Immediate(t *testing.T) {
	server := startMockServer(t)
	defer server.Close()

	testData := []byte("immediate data")
	testName := "key1"

	go func() {
		conn, err := server.Listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		var opCode uint16
		binary.Read(conn, binary.BigEndian, &opCode)
		if opCode != TSH_OP_GET {
			server.doneChan <- fmt.Errorf("expected OP_GET")
			return
		}
		readTshGetIt(conn) // consume header

		// Respond SUCCESS (immediate)
		resp1 := TshGetOt1{Status: SUCCESS, Error: 0}
		binary.Write(conn, binary.BigEndian, resp1)

		// Send Tuple Details
		var out2 TshGetOt2
		copy(out2.Name[:], []byte(testName))
		out2.Length = uint32(len(testData))
		writeTshGetOt2(conn, out2)

		// Send Data
		conn.Write(testData)
		server.doneChan <- nil
	}()

	client := &TupleSpaceClient{
		TshAddr: server.Addr,
		HostIP:  localhostIP(),
		AppId:   "testApp",
	}

	data, err := client.TsGet(testName)
	if err != nil {
		t.Fatalf("TsGet failed: %v", err)
	}

	if serverErr := <-server.doneChan; serverErr != nil {
		t.Fatalf("Server error: %v", serverErr)
	}

	if !reflect.DeepEqual(data, testData) {
		t.Errorf("Data mismatch. Got %s, want %s", data, testData)
	}
}

// TestTsRead_Delayed verifies TupleSpaceClient.TsRead's "delayed" path: the mock
// server responds to TSH_OP_READ with TshGetOt1{Status: FAILURE} and closes the
// request connection, then (after a short delay) dials back the client's local
// callback listener (using the port advertised in the request) to deliver the tuple
// details and data. TsRead is expected to block on its listener and return the data
// it receives there.
func TestTsRead_Delayed(t *testing.T) {
	server := startMockServer(t)
	defer server.Close()

	testData := []byte("delayed data")
	testName := "key2"

	go func() {
		conn1, err := server.Listener.Accept()
		if err != nil {
			return
		}

		var opCode uint16
		binary.Read(conn1, binary.BigEndian, &opCode)
		if opCode != TSH_OP_READ {
			server.doneChan <- fmt.Errorf("expected OP_READ")
			conn1.Close()
			return
		}
		getIt, _ := readTshGetIt(conn1)

		// Respond FAILURE (wait)
		resp1 := TshGetOt1{Status: FAILURE, Error: 0}
		binary.Write(conn1, binary.BigEndian, resp1)
		conn1.Close() // Close request connection

		time.Sleep(50 * time.Millisecond)

		// Callback
		clientAddr := fmt.Sprintf("127.0.0.1:%d", getIt.Port)
		conn2, err := net.Dial("tcp", clientAddr)
		if err != nil {
			server.doneChan <- fmt.Errorf("callback failed: %v", err)
			return
		}
		defer conn2.Close()

		var out2 TshGetOt2
		copy(out2.Name[:], []byte(testName))
		out2.Length = uint32(len(testData))
		writeTshGetOt2(conn2, out2)

		conn2.Write(testData)
		server.doneChan <- nil
	}()

	client := &TupleSpaceClient{
		TshAddr: server.Addr,
		HostIP:  localhostIP(),
		AppId:   "testApp",
	}

	data, err := client.TsRead(testName)
	if err != nil {
		t.Fatalf("TsRead failed: %v", err)
	}

	if serverErr := <-server.doneChan; serverErr != nil {
		t.Fatalf("Server error: %v", serverErr)
	}

	if !reflect.DeepEqual(data, testData) {
		t.Errorf("Data mismatch. Got %s, want %s", data, testData)
	}
}

// TestStructSizes is a basic sanity check that TshPutIt and TshGetIt, including their
// explicit padding fields, marshal (via binary.Size) to exactly 212 bytes each,
// matching the expected on-wire size of the corresponding C structs.
func TestStructSizes(t *testing.T) {
	// tsh_put_it size in C?
	// 64 + 128 + 2 + 2(pad) + 4 + 2 + 2(pad) + 4 + 4 = 212 bytes?
	// TshPutIt Go struct:
	// AppId[64] + Name[128] + Priority(2) + Pad(2) + Host(4) + Port(2) + Pad(2) + Length(4) + ProcId(4)
	// = 64+128+4+8+4+4 = 212 bytes. Correct.

	// Check via binary.Size if possible, but binary.Size doesn't account for `_` fields correctly in some contexts or maybe it does?
	// It does.
	p := TshPutIt{}
	sz := binary.Size(p)
	if sz != 212 {
		t.Errorf("TshPutIt size mismatch. Got %d, want 212", sz)
	}

	g := TshGetIt{}
	// AppId[64] + Expr[128] + Host(4) + Port(2) + Pad(2) + Length(4) + ProcId(4) + CidPort(2) + Pad(2)
	// 64+128+6+2+4+4+2+2 = 212 bytes.
	gsz := binary.Size(g)
	if gsz != 212 {
		t.Errorf("TshGetIt size mismatch. Got %d, want 212", gsz)
	}
}

// TestUvrCores verifies UvrCores correctly sums cores and counts nodes from a
// well-formed "<ip> <cores>" hosts file, and separately verifies that a missing hosts
// file is treated as zero cores/zero nodes with no error (rather than a failure).
func TestUvrCores(t *testing.T) {
	// Create a temporary hosts file
	content := `192.168.1.1 4
192.168.1.2 8
192.168.1.3 2
`
	tmpfile, err := os.CreateTemp("", "hosts")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name()) // clean up

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	cores, nodes, err := UvrCores(tmpfile.Name())
	if err != nil {
		t.Fatalf("UvrCores failed: %v", err)
	}

	expectedCores := 14
	expectedNodes := 3

	if cores != expectedCores {
		t.Errorf("Expected %d cores, got %d", expectedCores, cores)
	}
	if nodes != expectedNodes {
		t.Errorf("Expected %d nodes, got %d", expectedNodes, nodes)
	}

	// Test missing file
	c, n, err := UvrCores("non_existent_file")
	if err != nil {
		t.Errorf("Expected no error for missing file, got %v", err)
	}
	if c != 0 || n != 0 {
		t.Errorf("Expected 0/0 for missing file, got %d/%d", c, n)
	}
}
