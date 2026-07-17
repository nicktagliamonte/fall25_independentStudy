package net

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
)

// source ip, return port #, message id, content id, replication vector (local, mid, far)

// Package note: despite the filename ("tslib"), this file has nothing to do with
// Tailscale — it is a Go client for a separate C "tuple space handler" (TSH) daemon
// protocol, mirroring a C header referred to as synergy.h. "TS" here stands for Tuple
// Space, and "UVR" refers to the unidirectional-vector-ring traversal used by the
// external SNG coordination system when a put doesn't resolve locally. This client is
// used by the CLI ("ts put/get/read" subcommands in pkg/node/run.go) to talk to that
// daemon; it is unrelated to Tailscale IP discovery (see docs/EC2_TAILSCALE.md), which
// this repository does not appear to implement anywhere in Go code.

// Constants from synergy.h mirror the C protocol's opcode, status, and error-code
// values, and fixed field widths, so wire-compatible messages can be constructed here.
const (
	// TSH_OP_PUT requests storing a tuple.
	TSH_OP_PUT uint16 = 401
	// TSH_OP_GET requests retrieving (and consuming) a tuple matching an expression.
	TSH_OP_GET uint16 = 402
	// TSH_OP_READ requests retrieving (without consuming) a tuple matching an expression.
	TSH_OP_READ uint16 = 403
	// TSH_OP_UVRPut3 requests the final "store" phase of a put that was not resolved
	// locally and completed a UVR traversal (see TsPut phase 3).
	TSH_OP_UVRPut3 uint16 = 415
	// FAILURE is the status value indicating an operation did not succeed (or, in the
	// initial TsPut response, that the tuple was not immediately consumed locally and a
	// UVR traversal is required).
	FAILURE int32 = 0
	// SUCCESS is the status value indicating an operation completed immediately.
	SUCCESS int32 = 1
	// TSPUT_ER is the generic error return code for TsPut failures, mirroring the C
	// client's convention.
	TSPUT_ER int = -106
	// TSGET_ER is the generic error return code for TsGet failures.
	TSGET_ER int = -107
	// TSREAD_ER is the generic error return code for TsRead failures.
	TSREAD_ER int = -108

	// NAME_LEN2 is the fixed byte width of application-ID fields on the wire.
	NAME_LEN2 = 64
	// TUPLENAME_LEN is the fixed byte width of tuple name/expression fields on the wire.
	TUPLENAME_LEN = 128
)

// TshPutIt corresponds to the C tsh_put_it struct: the request header sent to the TSH
// daemon (and, in phase 3, echoed back to it) to store a tuple. The `_` fields are
// explicit padding matching typical C struct alignment (4-byte); the struct is
// serialized field-by-field in big-endian via writeTshPutIt rather than relying on Go's
// in-memory layout, so the padding fields exist mainly for size/documentation purposes
// (binary.Size(TshPutIt{}) == 212, verified by TestStructSizes in tslib_test.go).
type TshPutIt struct {
	// AppId is the requesting application's identifier, null-padded to NAME_LEN2 bytes.
	AppId [NAME_LEN2]byte
	// Name is the tuple's name, null-padded to TUPLENAME_LEN bytes.
	Name     [TUPLENAME_LEN]byte
	Priority uint16
	_        [2]byte // Padding for alignment
	// Host is the caller's IP (as reported by TupleSpaceClient.HostIP) for TSH to use
	// when calling back for phase 2 (UVR) callbacks.
	Host uint32
	// Port is the local callback listener's port (see getTempListener), for TSH to
	// dial back to when a UVR match is found or the traversal completes.
	Port   uint16
	_      [2]byte // Padding for alignment
	Length uint32
	// ProcId is the calling process's PID (os.Getpid()), included for parity with the
	// C client; not used for matching/correlation in this Go implementation.
	ProcId int32
}

// TshGetIt corresponds to the C tsh_get_it struct: the request header sent to the TSH
// daemon for both TsGet (consuming) and TsRead (non-consuming) operations. Serialized
// via writeTshGetIt; binary.Size(TshGetIt{}) == 212 (see TestStructSizes).
type TshGetIt struct {
	// AppId is the requesting application's identifier, null-padded to NAME_LEN2 bytes.
	AppId [NAME_LEN2]byte
	// Expr is the tuple name/expression to match, null-padded to TUPLENAME_LEN bytes.
	Expr [TUPLENAME_LEN]byte
	// Host is the caller's IP, for TSH's delayed-response callback.
	Host uint32
	// Port is the local callback listener's port, for TSH's delayed-response callback.
	Port   uint16
	_      [2]byte // Padding for alignment (after port/before len)
	Length uint32
	ProcId int32
	// CidPort is present in the C struct's layout (unused by TsGet/TsRead in this
	// implementation — always zero-valued here).
	CidPort uint16
	_       [2]byte // Final padding to 212 bytes
}

// TshPutOt corresponds to the C tsh_put_ot struct: the immediate response to a
// TSH_OP_PUT or TSH_OP_UVRPut3 request. Status is FAILURE or SUCCESS; Error carries an
// operation-specific result/error code (e.g. the value ultimately returned by TsPut).
type TshPutOt struct {
	Status int32
	Error  int32
}

// TshGetOt1 corresponds to the C tsh_get_ot1 struct (identical layout to TshPutOt):
// the immediate response to a TSH_OP_GET/TSH_OP_READ request, indicating whether the
// tuple is available now (SUCCESS, data follows on the same connection) or must be
// awaited via callback (FAILURE, caller waits on its listener).
type TshGetOt1 struct {
	Status int32
	Error  int32
}

// TshGetOt2 corresponds to the C tsh_get_ot2 struct: the tuple-details header sent
// (on either the original connection or the callback connection) immediately before
// the tuple's raw data bytes, for both TsGet and TsRead.
type TshGetOt2 struct {
	AppId    [NAME_LEN2]byte
	Name     [TUPLENAME_LEN]byte
	Length   uint32
	Priority uint16
	_        [2]byte // Padding
}

// UvrReturnStruct corresponds to the C tsh_put3_it struct: the message TSH sends back
// (dialing the caller's callback listener) during a TsPut's phase-2 UVR wait loop,
// once per traversal step, reporting either a match (Status == SUCCESS, caller should
// send the tuple to this connection) or end-of-traversal (Status == FAILURE).
type UvrReturnStruct struct {
	Host    uint32
	Port    uint16
	_       [2]byte // Padding for alignment
	Request int32
	// Status is FAILURE to signal end-of-traversal (no match found), or non-FAILURE to
	// signal a match was found and the tuple should be sent on this connection.
	Status int32
}

// TupleSpaceClient holds configuration for talking to a single TSH (tuple space
// handler) daemon instance. It is stateless/reusable across calls — each of
// TsPut/TsGet/TsRead opens its own connection(s).
type TupleSpaceClient struct {
	TshAddr string // "host:port" of the TSH daemon
	HostIP  uint32 // Local IP address to report to TSH (ipv4 as int)
	AppId   string
}

// getTempListener opens a TCP listener bound to an OS-assigned ephemeral port on all
// interfaces (":0"), used as the local callback endpoint TSH connects back to for
// delayed responses (TsGet/TsRead) or UVR traversal callbacks (TsPut). Returns the
// open net.Listener (caller is responsible for closing it), the assigned local port
// number, and a non-nil error if binding the listener or reading back its assigned
// port fails.
func getTempListener() (net.Listener, int, error) {
	listener, err := net.Listen("tcp", ":0") // bind to any available port
	if err != nil {
		return nil, 0, fmt.Errorf("failed to bind return socket: %w", err)
	}

	// Get the assigned port
	_, portStr, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		return nil, 0, fmt.Errorf("failed to get reliable port: %w", err)
	}
	localPort := 0
	fmt.Sscanf(portStr, "%d", &localPort)
	return listener, localPort, nil
}

// writeTshPutIt serializes s to w field-by-field in big-endian order (writing explicit
// zero-padding between fields to match the C struct layout), rather than relying on
// binary.Write(w, order, s) directly on the whole struct. Note: the individual
// binary.Write calls' errors are discarded (not checked) — the function always returns
// nil regardless of whether any underlying write failed; callers relying on this
// error return to detect a failed write will not be able to (a subsequent read/write
// on the same connection will typically surface the failure instead).
func writeTshPutIt(w io.Writer, s TshPutIt) error {
	binary.Write(w, binary.BigEndian, s.AppId)
	binary.Write(w, binary.BigEndian, s.Name)
	binary.Write(w, binary.BigEndian, s.Priority)
	binary.Write(w, binary.BigEndian, [2]byte{}) // Padding
	binary.Write(w, binary.BigEndian, s.Host)
	binary.Write(w, binary.BigEndian, s.Port)
	binary.Write(w, binary.BigEndian, [2]byte{}) // Padding
	binary.Write(w, binary.BigEndian, s.Length)
	binary.Write(w, binary.BigEndian, s.ProcId)
	return nil
}

// writeTshGetIt serializes s to w field-by-field in big-endian order with explicit
// padding, mirroring writeTshPutIt. Like writeTshPutIt, the individual binary.Write
// errors are discarded and the function always returns nil.
func writeTshGetIt(w io.Writer, s TshGetIt) error {
	binary.Write(w, binary.BigEndian, s.AppId)
	binary.Write(w, binary.BigEndian, s.Expr)
	binary.Write(w, binary.BigEndian, s.Host)
	binary.Write(w, binary.BigEndian, s.Port)
	binary.Write(w, binary.BigEndian, [2]byte{}) // Padding
	binary.Write(w, binary.BigEndian, s.Length)
	binary.Write(w, binary.BigEndian, s.ProcId)
	binary.Write(w, binary.BigEndian, s.CidPort)
	binary.Write(w, binary.BigEndian, [2]byte{}) // Padding
	return nil
}

// TsPut stores a tuple named tpname with value tpvalue in the tuple space managed by
// the TSH daemon at c.TshAddr, implementing (in Go) the same three-phase protocol as
// the C tsput client:
//
//  1. Local attempt: connects to c.TshAddr, sends TSH_OP_PUT followed by a TshPutIt
//     header (built from tpname/tpvalue/c.AppId/c.HostIP and the local callback
//     listener's port) and the raw tuple bytes, then reads a TshPutOt response. If
//     in.Status != FAILURE, the put was resolved locally (consumed by a waiting getter
//     or single-node stored) and TsPut returns (int(in.Error), nil) immediately.
//
//  2. UVR wait loop (only if phase 1 returned FAILURE): TsPut closes the phase-1
//     connection and blocks on its local callback listener (from getTempListener),
//     accepting connections from TSH as it walks the UVR ring. For each callback it
//     reads a UvrReturnStruct; a Status of FAILURE means the traversal ended with no
//     match (loop breaks, proceeds to phase 3). Any other status means a match was
//     found: TsPut writes the TshPutIt header and the tuple value directly to that
//     callback connection. If the match's Request equals TSH_OP_GET (the tuple was
//     consumed by the matching getter), TsPut returns (TSPUT_ER, nil) — note this
//     specific case returns TSPUT_ER (a negative "error" code) even though it
//     represents a successful hand-off; the code comment marks this as a known
//     oddity inherited from the C implementation and expects the caller to treat it
//     as success. Otherwise the loop continues waiting for further callbacks.
//
//  3. Store phase (only reached if the UVR loop exhausted with no consuming match):
//     TsPut reconnects to c.TshAddr, sends TSH_OP_UVRPut3 with the same header and
//     value, and returns (int(inFinal.Error), nil) from the final TshPutOt response.
//
// Return value: on any network/protocol error at any phase, returns (TSPUT_ER, err)
// with a non-nil, wrapped error describing which step failed. On protocol success
// (any phase), returns (code, nil) where code is an operation-specific result/error
// code from the TSH daemon (not necessarily indicating success in the Go/error sense —
// callers must interpret code themselves, mirroring the C client's convention).
func (c *TupleSpaceClient) TsPut(tpname string, tpvalue []byte) (int, error) {
	tpsize := uint32(len(tpvalue))

	// 1. Setup local listener for return connection (Phase 2)
	listener, localPort, err := getTempListener()
	if err != nil {
		return TSPUT_ER, err
	}
	defer listener.Close()

	// 2. Connect to TSH (Phase 1)
	conn, err := net.Dial("tcp", c.TshAddr)
	if err != nil {
		return TSPUT_ER, fmt.Errorf("connectTsh::get_socket/do_connect: %w", err)
	}

	// Send OpCode
	opCode := TSH_OP_PUT
	if err := binary.Write(conn, binary.BigEndian, opCode); err != nil {
		conn.Close()
		return TSPUT_ER, fmt.Errorf("tsput: Op code send error: %w", err)
	}

	// Prepare Output Struct
	var out TshPutIt
	copy(out.AppId[:], []byte(c.AppId))
	copy(out.Name[:], []byte(tpname))
	out.Priority = 1 // Saved for later implementation
	out.Length = tpsize
	out.Host = c.HostIP
	out.Port = uint16(localPort)
	out.ProcId = int32(os.Getpid())

	// Send TshPutIt struct
	if err := writeTshPutIt(conn, out); err != nil {
		conn.Close()
		return TSPUT_ER, fmt.Errorf("tsput: Length/Struct send error: %w", err)
	}

	// Send Tuple Value
	if _, err := conn.Write(tpvalue); err != nil {
		conn.Close()
		return TSPUT_ER, fmt.Errorf("tsput: Value send error: %w", err)
	}

	// Read Result
	var in TshPutOt
	if err := binary.Read(conn, binary.BigEndian, &in); err != nil {
		conn.Close()
		return TSPUT_ER, fmt.Errorf("tsput: read status error: %w", err)
	}

	if in.Status != FAILURE {
		// Local consumed or single node stored
		conn.Close()
		return int(in.Error), nil
	}

	conn.Close() // Free tsh socket

	// 3. Phase 2: UVR Wait Loop
	var uvrReturn UvrReturnStruct

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			return TSPUT_ER, fmt.Errorf("OpPut::uvrReturn accept failure: %w", err)
		}

		// Read uvrReturn struct
		if err := binary.Read(clientConn, binary.BigEndian, &uvrReturn.Host); err != nil {
			clientConn.Close()
			return TSPUT_ER, err
		}
		if err := binary.Read(clientConn, binary.BigEndian, &uvrReturn.Port); err != nil {
			clientConn.Close()
			return TSPUT_ER, err
		}
		// Skip padding
		ignore := make([]byte, 2)
		io.ReadFull(clientConn, ignore)

		if err := binary.Read(clientConn, binary.BigEndian, &uvrReturn.Request); err != nil {
			clientConn.Close()
			return TSPUT_ER, err
		}
		if err := binary.Read(clientConn, binary.BigEndian, &uvrReturn.Status); err != nil {
			clientConn.Close()
			return TSPUT_ER, err
		}

		if uvrReturn.Status == FAILURE {
			// End of traversal
			clientConn.Close()
			break
		}

		// uvrReturn Match found
		// Send tuple header to client
		buf := new(bytes.Buffer)
		writeTshPutIt(buf, out)

		if _, err := clientConn.Write(buf.Bytes()); err != nil {
			clientConn.Close()
			return TSPUT_ER, fmt.Errorf("Direct send to client header failure: %w", err)
		}

		// Send tuple value
		if _, err := clientConn.Write(tpvalue); err != nil {
			clientConn.Close()
			return TSPUT_ER, fmt.Errorf("Direct send to client content failure: %w", err)
		}

		if uvrReturn.Request == int32(TSH_OP_GET) {
			// Consumed
			clientConn.Close()
			return TSPUT_ER, nil // Technically TSPUT_ER is what C returns, which is confusing if success. Assuming caller handles.
		}

		clientConn.Close()
		// Loop back to accept next
	}

	// 4. Phase 3: Store Tuple
	// Connect to TSH again
	conn3, err := net.Dial("tcp", c.TshAddr)
	if err != nil {
		return TSPUT_ER, fmt.Errorf("connectTsh3::do_connect: %w", err)
	}
	defer conn3.Close()

	opCode3 := TSH_OP_UVRPut3
	if err := binary.Write(conn3, binary.BigEndian, opCode3); err != nil {
		return TSPUT_ER, fmt.Errorf("Store tuple op failure: %w", err)
	}

	// Send tuple header
	if err := writeTshPutIt(conn3, out); err != nil {
		return TSPUT_ER, fmt.Errorf("Store tuple header failure: %w", err)
	}

	// Send tuple body
	if _, err := conn3.Write(tpvalue); err != nil {
		return TSPUT_ER, fmt.Errorf("Store tuple body failure: %w", err)
	}

	// Read final status
	var inFinal TshPutOt
	if err := binary.Read(conn3, binary.BigEndian, &inFinal); err != nil {
		return TSPUT_ER, fmt.Errorf("PUT final: read status error: %w", err)
	}

	return int(inFinal.Error), nil
}

// TsGet retrieves and consumes (removes from the tuple space) the tuple matching
// expression tpname from the TSH daemon at c.TshAddr, mirroring the C tsgetv function
// (which allocates its own buffer, unlike C tsget which takes a caller-provided
// buffer — this Go version always allocates, which is why it corresponds to tsgetv).
//
// Protocol: opens a local callback listener (getTempListener), connects to c.TshAddr,
// sends TSH_OP_GET with a TshGetIt header (tpname as Expr; Length is not populated —
// see inline comments in the source about ambiguity with the C client's handling of
// unknown-size wildcard gets), and reads a TshGetOt1 response.
//   - If Status == SUCCESS, the tuple is available immediately and its details/data
//     follow on the same connection.
//   - Otherwise, the request connection is closed and TsGet blocks accepting a
//     callback connection on its local listener, on which TSH later sends the tuple
//     details/data once available.
//
// In both cases, TsGet then reads a TshGetOt2 header (field-by-field, matching the C
// struct's padding) followed by exactly in2.Length bytes of tuple data via
// io.ReadFull.
//
// Returns the tuple's data as a newly allocated []byte, or (nil, err) with a non-nil,
// wrapped error if listener setup, dialing, encoding the request, or any read (status,
// header, or data) fails.
func (c *TupleSpaceClient) TsGet(tpname string) ([]byte, error) {
	// 1. Setup local listener just in case we need to wait
	listener, localPort, err := getTempListener()
	if err != nil {
		return nil, fmt.Errorf("get_socket: %w", err)
	}
	defer listener.Close()

	// 2. Connect to TSH
	conn, err := net.Dial("tcp", c.TshAddr)
	if err != nil {
		return nil, fmt.Errorf("connectTsh: %w", err)
	}

	opCode := TSH_OP_GET
	if err := binary.Write(conn, binary.BigEndian, opCode); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tsget: Op code send error: %w", err)
	}

	var out TshGetIt
	copy(out.AppId[:], []byte(c.AppId))
	copy(out.Expr[:], []byte(tpname))
	out.Host = c.HostIP
	out.Port = uint16(localPort)
	out.ProcId = int32(os.Getpid())
	// Length ignored in C tsget (commented out), but tsgetv sends it as *tpsize if known?
	// If input size is unknown (-1 in C logic kind of?), we send 0 or random?
	// C tsgetv does `out.len = htonl(*tpsize)`.
	// Since we are doing a GET, we typically don't know the size unless we are matching specific size?
	// Usually 0 if wildcard.

	if err := writeTshGetIt(conn, out); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tsget: Length/Struct send error: %w", err)
	}

	// Read Result 1
	var in1 TshGetOt1
	if err := binary.Read(conn, binary.BigEndian, &in1); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tsget: read status error: %w", err)
	}

	var dataSocket net.Conn

	if in1.Status == SUCCESS {
		// Immediately available on existing socket
		dataSocket = conn
	} else {
		// Not immediately available, wait on listener
		conn.Close() // Close TSH request connection

		// Wait for connection
		dataSocket, err = listener.Accept()
		if err != nil {
			return nil, fmt.Errorf("tsget: accept failure: %w", err)
		}
	}
	defer dataSocket.Close()

	// Read Tuple Details (TshGetOt2)
	// TshGetOt2 has padding.
	var in2 TshGetOt2
	if err := binary.Read(dataSocket, binary.BigEndian, &in2.AppId); err != nil {
		return nil, fmt.Errorf("tsget: read result error: %w", err)
	}
	if err := binary.Read(dataSocket, binary.BigEndian, &in2.Name); err != nil {
		return nil, fmt.Errorf("tsget: read result error: %w", err)
	}
	if err := binary.Read(dataSocket, binary.BigEndian, &in2.Length); err != nil {
		return nil, fmt.Errorf("tsget: read result error: %w", err)
	}
	if err := binary.Read(dataSocket, binary.BigEndian, &in2.Priority); err != nil {
		return nil, fmt.Errorf("tsget: read result error: %w", err)
	}
	// Skip padding (2 bytes)
	io.ReadFull(dataSocket, make([]byte, 2))

	actualSize := in2.Length
	// TODO: verify tpname matches out.Name if needed, but TSH does matching.

	// Read Data
	data := make([]byte, actualSize)
	if _, err := io.ReadFull(dataSocket, data); err != nil {
		return nil, fmt.Errorf("tsget: tuple read error: %w", err)
	}

	return data, nil
}

// TsRead retrieves (without consuming/removing) the tuple matching expression tpname
// from the TSH daemon at c.TshAddr. It follows the exact same protocol as TsGet (see
// its doc comment) except it sends TSH_OP_READ instead of TSH_OP_GET, so the tuple
// remains in the tuple space after this call.
//
// Returns the tuple's data as a newly allocated []byte, or (nil, err) with a non-nil,
// wrapped error if listener setup, dialing, encoding the request, or any read (status,
// header, or data) fails.
func (c *TupleSpaceClient) TsRead(tpname string) ([]byte, error) {
	// 1. Setup local listener just in case we need to wait
	listener, localPort, err := getTempListener()
	if err != nil {
		return nil, fmt.Errorf("get_socket: %w", err)
	}
	defer listener.Close()

	// 2. Connect to TSH
	conn, err := net.Dial("tcp", c.TshAddr)
	if err != nil {
		return nil, fmt.Errorf("connectTsh: %w", err)
	}

	opCode := TSH_OP_READ
	if err := binary.Write(conn, binary.BigEndian, opCode); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tsread: Op code send error: %w", err)
	}

	var out TshGetIt
	copy(out.AppId[:], []byte(c.AppId))
	copy(out.Expr[:], []byte(tpname))
	out.Host = c.HostIP
	out.Port = uint16(localPort)
	out.ProcId = int32(os.Getpid())

	if err := writeTshGetIt(conn, out); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tsread: Length/Struct send error: %w", err)
	}

	// Read Result 1
	var in1 TshGetOt1
	if err := binary.Read(conn, binary.BigEndian, &in1); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tsread: read status error: %w", err)
	}

	var dataSocket net.Conn

	if in1.Status == SUCCESS {
		// Immediately available on existing socket
		dataSocket = conn
	} else {
		// Not immediately available, wait on listener
		conn.Close() // Close TSH request connection

		// Wait for connection
		dataSocket, err = listener.Accept()
		if err != nil {
			return nil, fmt.Errorf("tsread: accept failure: %w", err)
		}
	}
	defer dataSocket.Close()

	// Read Tuple Details (TshGetOt2)
	var in2 TshGetOt2
	if err := binary.Read(dataSocket, binary.BigEndian, &in2.AppId); err != nil {
		return nil, fmt.Errorf("tsread: read result error: %w", err)
	}
	if err := binary.Read(dataSocket, binary.BigEndian, &in2.Name); err != nil {
		return nil, fmt.Errorf("tsread: read result error: %w", err)
	}
	if err := binary.Read(dataSocket, binary.BigEndian, &in2.Length); err != nil {
		return nil, fmt.Errorf("tsread: read result error: %w", err)
	}
	if err := binary.Read(dataSocket, binary.BigEndian, &in2.Priority); err != nil {
		return nil, fmt.Errorf("tsread: read result error: %w", err)
	}
	// Skip padding (2 bytes)
	io.ReadFull(dataSocket, make([]byte, 2))

	actualSize := in2.Length

	// Read Data
	data := make([]byte, actualSize)
	if _, err := io.ReadFull(dataSocket, data); err != nil {
		return nil, fmt.Errorf("tsread: tuple read error: %w", err)
	}

	return data, nil
}

// UvrCores reads a whitespace-separated "hosts" file at hostsPath, where each line is
// expected to be "<ip> <cores>" (parsed via fmt.Fscanf(file, "%s %d", ...)), and sums
// the total core count and counts the number of host lines successfully parsed.
// Parsing stops at the first line that fails to scan as "<string> <int>" (including
// EOF) — it does not skip and continue past malformed lines; totals reflect only the
// lines parsed before that point (mirroring the C implementation's fscanf-return-based
// loop, per the inline comment).
//
// If hostsPath does not exist, this is treated as "no hosts configured": returns
// (0, 0, nil), not an error. Any other error opening the file (e.g. permissions)
// returns (0, 0, err) with a non-nil, wrapped error.
//
// Returns (totalCores, totalNodes, error).
func UvrCores(hostsPath string) (int, int, error) {
	file, err := os.Open(hostsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("failed to open hosts file: %w", err)
	}
	defer file.Close()

	totalCores := 0
	totalNodes := 0

	var ip string
	var cores int

	for {
		_, err := fmt.Fscanf(file, "%s %d", &ip, &cores)
		if err != nil {
			if err == io.EOF {
				break
			}
			// Skip malformed lines or handle error
			// C implementation uses fscanf > 0, which just stops on mismatch or EOF
			// We'll mimic stopping on error for now, or we could continue.
			// Ideally we break on EOF and ignore whitespace issues.
			break
		}
		totalCores += cores
		totalNodes++
	}

	return totalCores, totalNodes, nil
}
