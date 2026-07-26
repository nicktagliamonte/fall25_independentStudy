// Package tshclient is a Go client for the legacy C "TSH" (tuple space host)
// daemon protocol, reimplementing the wire format of tsh_put_it/tsh_get_it and
// related C structs (see synergy.h) so this process can PUT/GET/READ tuples
// against an existing TSH daemon over a plain TCP socket. This is unrelated to
// the content-addressed storage system's own Put/Get flow, and unrelated to
// internal/tuplespace's DHT/P2P-backed TupleSpace implementations; it is a
// standalone bridge to an older, separate tuple-space system. As of this
// package's introduction it has no callers elsewhere in this module (verified
// via repo-wide grep) — it is kept for potential future use bridging to a live
// TSH daemon, not currently wired into any running node.
package tshclient

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
)

// source ip, return port #, message id, content id, replication vector (local, mid, far)

// Constants from synergy.h. TSH_OP_* are wire opcodes sent as the first field of
// each request; FAILURE/SUCCESS are status codes returned by the daemon; TS*_ER
// are the Go-side error codes returned by this client on failure (mirroring the
// original C client's negative error constants); NAME_LEN2/TUPLENAME_LEN are the
// fixed-size byte-array lengths used by the C wire structs.
const (
	// TSH_OP_PUT is the opcode for a tuple PUT request.
	TSH_OP_PUT uint16 = 401
	// TSH_OP_GET is the opcode for a tuple GET (consuming) request.
	TSH_OP_GET uint16 = 402
	// TSH_OP_READ is the opcode for a tuple READ (non-consuming) request.
	TSH_OP_READ uint16 = 403
	// TSH_OP_UVRPut3 is the opcode for the phase-3 "store tuple" step of a PUT
	// that fell through the UVR (unmatched-value-request) wait loop.
	TSH_OP_UVRPut3 uint16 = 415
	// FAILURE is the daemon's status code indicating a request did not complete immediately.
	FAILURE int32 = 0
	// SUCCESS is the daemon's status code indicating a request completed immediately.
	SUCCESS int32 = 1
	// TSPUT_ER is the Go-side error/status code returned by TsPut on failure.
	TSPUT_ER int = -106
	// TSGET_ER is reserved for TsGet failure signaling (mirrors the C client's constant).
	TSGET_ER int = -107
	// TSREAD_ER is reserved for TsRead failure signaling (mirrors the C client's constant).
	TSREAD_ER int = -108

	// NAME_LEN2 is the fixed byte length of the AppId field in the C wire structs.
	NAME_LEN2 = 64
	// TUPLENAME_LEN is the fixed byte length of the tuple name/expression field in the C wire structs.
	TUPLENAME_LEN = 128
)

// TshPutIt corresponds to tsh_put_it C struct
// Note: We use explicit padding to match C struct alignment (4-byte alignment typically).
// It is the request header sent to TSH for a PUT operation, written field-by-field
// via writeTshPutIt (not via binary.Write on the struct directly) to control padding
// and byte order precisely.
type TshPutIt struct {
	// AppId is the requesting application's identifier, null-padded to NAME_LEN2 bytes.
	AppId [NAME_LEN2]byte
	// Name is the tuple's name, null-padded to TUPLENAME_LEN bytes.
	Name [TUPLENAME_LEN]byte
	// Priority is the tuple's priority (currently always set to 1; reserved for future use).
	Priority uint16
	_        [2]byte // Padding for alignment
	// Host is this client's IP address (as reported to TSH) in network byte order.
	Host uint32
	// Port is the local TCP port this client is listening on for the UVR return connection.
	Port uint16
	_    [2]byte // Padding for alignment
	// Length is the byte length of the tuple value that follows the header on the wire.
	Length uint32
	// ProcId is this process's OS process ID, included for daemon-side bookkeeping.
	ProcId int32
}

// TshGetIt corresponds to tsh_get_it C struct. It is the request header sent to
// TSH for GET and READ operations, written field-by-field via writeTshGetIt.
type TshGetIt struct {
	// AppId is the requesting application's identifier, null-padded to NAME_LEN2 bytes.
	AppId [NAME_LEN2]byte
	// Expr is the tuple name/expression to match, null-padded to TUPLENAME_LEN bytes.
	Expr [TUPLENAME_LEN]byte
	// Host is this client's IP address (as reported to TSH) in network byte order.
	Host uint32
	// Port is the local TCP port this client is listening on if the match is not immediate.
	Port uint16
	_    [2]byte // Padding for alignment (after port/before len)
	// Length is unused by this client's GET/READ requests (kept for wire compatibility).
	Length uint32
	// ProcId is this process's OS process ID, included for daemon-side bookkeeping.
	ProcId int32
	// CidPort is reserved for a content-ID-based port, unused by this client.
	CidPort uint16
	_       [2]byte // Final padding to 212 bytes
}

// TshPutOt corresponds to tsh_put_ot C struct. It is TSH's immediate response to a
// PUT request.
type TshPutOt struct {
	// Status is FAILURE if the daemon could not satisfy the request immediately
	// (requiring the phase-2 UVR wait loop), or a non-FAILURE code otherwise.
	Status int32
	// Error carries the daemon's result/error code for the request.
	Error int32
}

// TshGetOt1 corresponds to tsh_get_ot1 C struct (same as TshPutOt). It is TSH's
// immediate response to a GET/READ request, indicating whether a match is
// available now (SUCCESS) or the client must wait on its listener (otherwise).
type TshGetOt1 struct {
	// Status is SUCCESS if a matching tuple is immediately available on the
	// existing connection, or a different value if the client must wait.
	Status int32
	// Error carries the daemon's result/error code for the request.
	Error int32
}

// TshGetOt2 corresponds to tsh_get_ot2 C struct. It describes the tuple actually
// returned for a GET/READ request, read field-by-field from the data socket.
type TshGetOt2 struct {
	// AppId is the owning application's identifier for the returned tuple.
	AppId [NAME_LEN2]byte
	// Name is the returned tuple's name.
	Name [TUPLENAME_LEN]byte
	// Length is the byte length of the tuple value that follows on the wire.
	Length uint32
	// Priority is the returned tuple's priority.
	Priority uint16
	_        [2]byte // Padding
}

// UvrReturnStruct corresponds to tsh_put3_it C struct. It is read from the
// client's temporary listener during the phase-2 UVR (unmatched-value-request)
// wait loop of TsPut, describing a waiting GET/READ requester to satisfy directly.
type UvrReturnStruct struct {
	// Host is the waiting requester's reported IP address.
	Host uint32
	// Port is the waiting requester's reported port.
	Port uint16
	_    [2]byte // Padding for alignment
	// Request is the waiting requester's original opcode (e.g. TSH_OP_GET), used
	// to decide whether the tuple is consumed after being delivered.
	Request int32
	// Status is FAILURE when there are no more waiting requesters (ends the UVR loop).
	Status int32
}

// TupleSpaceClient holds configuration for the tuple space operations
type TupleSpaceClient struct {
	// TshAddr is the "host:port" address of the TSH daemon to connect to.
	TshAddr string // "host:port" of the TSH daemon
	// HostIP is the local IP address to report to TSH, encoded as a big-endian uint32.
	HostIP uint32 // Local IP address to report to TSH (ipv4 as int)
	// AppId is this client's application identifier, sent with every request.
	AppId string
}

// getTempListener binds a TCP listener on an OS-assigned ("any available") port,
// used to receive the TSH daemon's UVR-return or delayed-match callback connection.
//
// Returns:
//   - net.Listener: the bound listener; callers must Close it.
//   - int: the OS-assigned local port number.
//   - error: non-nil if binding the listener or reading its assigned port fails.
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

// writeTshPutIt writes a TshPutIt's fields manually, in wire order with explicit
// padding, to handle C struct alignment correctly (rather than relying on
// binary.Write over the Go struct, whose layout is not guaranteed to match).
// Stops and returns the first error encountered from the underlying
// binary.Write calls, leaving the write partially complete on the wire.
//
// Parameters:
//   - w (io.Writer): destination for the encoded bytes (e.g. a net.Conn).
//   - s (TshPutIt): the struct to encode.
//
// Returns:
//   - error: non-nil if any field write fails, nil if all fields were written successfully.
func writeTshPutIt(w io.Writer, s TshPutIt) error {
	fields := []interface{}{
		s.AppId, s.Name, s.Priority, [2]byte{}, // Padding
		s.Host, s.Port, [2]byte{}, // Padding
		s.Length, s.ProcId,
	}
	for _, f := range fields {
		if err := binary.Write(w, binary.BigEndian, f); err != nil {
			return err
		}
	}
	return nil
}

// writeTshGetIt writes a TshGetIt's fields manually, in wire order with explicit
// padding, mirroring writeTshPutIt. Stops and returns the first error
// encountered from the underlying binary.Write calls, leaving the write
// partially complete on the wire.
//
// Parameters:
//   - w (io.Writer): destination for the encoded bytes (e.g. a net.Conn).
//   - s (TshGetIt): the struct to encode.
//
// Returns:
//   - error: non-nil if any field write fails, nil if all fields were written successfully.
func writeTshGetIt(w io.Writer, s TshGetIt) error {
	fields := []interface{}{
		s.AppId, s.Expr, s.Host, s.Port, [2]byte{}, // Padding
		s.Length, s.ProcId, s.CidPort, [2]byte{}, // Padding
	}
	for _, f := range fields {
		if err := binary.Write(w, binary.BigEndian, f); err != nil {
			return err
		}
	}
	return nil
}

// TsPut implements the tuple space put operation. It follows the legacy C tsput
// protocol in up to three phases: (1) send the PUT request and tuple value to TSH
// on a fresh connection; if the daemon reports immediate completion (Status !=
// FAILURE), returns its Error code directly; (2) otherwise, waits on a local
// listener for TSH to relay pending GET/READ requesters one at a time (the "UVR
// wait loop"), delivering the tuple header+value to each; if a requester is a
// consuming GET (opcode TSH_OP_GET), the tuple is considered consumed and TsPut
// returns immediately; (3) if the loop exhausts with no consuming GET, reconnects
// to TSH and issues a TSH_OP_UVRPut3 request to durably store the tuple, returning
// the daemon's final status.
//
// Parameters:
//   - tpname (string): the tuple's name.
//   - tpvalue ([]byte): the tuple's value/content.
//
// Returns:
//   - int: TSPUT_ER on any local/transport error; 0 on a successful consumed-by-GET
//     handoff (phase 2); otherwise the daemon-reported status/error code from
//     phase 1 or phase 3 (semantics defined by the TSH protocol, not this package).
//   - error: non-nil describing the failing step when a local/transport error occurs, nil otherwise.
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
		if err := writeTshPutIt(buf, out); err != nil {
			clientConn.Close()
			return TSPUT_ER, fmt.Errorf("Direct send to client header encode failure: %w", err)
		}

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
			// Consumed: the tuple was handed off directly to a waiting GET, which
			// is a successful Put outcome. Return 0 (success), not TSPUT_ER, so
			// callers checking the numeric code (not just err) don't misread a
			// successful handoff as a failure.
			clientConn.Close()
			return 0, nil
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

// TsGet implements the tuple space get operation (consuming).
// Returns the data in a newly allocated slice (like C tsgetv).
// To mimic C tsget (buffer provided), one could use a different signature, but this is safer.
// It sends a TSH_OP_GET request; if the daemon reports SUCCESS immediately, the
// tuple header and value are read from the same connection, otherwise this client
// waits on its local listener for TSH to deliver the match once available.
//
// Parameters:
//   - tpname (string): the tuple name/expression to match.
//
// Returns:
//   - []byte: the matched tuple's value, newly allocated.
//   - error: non-nil describing the failing step if the request, wait, or read fails.
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

// TsRead implements the tuple space read operation (non-consuming). It behaves
// identically to TsGet except it sends a TSH_OP_READ opcode, so a matched tuple
// is left in the tuple space for future GET/READ calls.
//
// Parameters:
//   - tpname (string): the tuple name/expression to match.
//
// Returns:
//   - []byte: the matched tuple's value, newly allocated.
//   - error: non-nil describing the failing step if the request, wait, or read fails.
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

// UvrCores calculates the total number of cores deployed at runtime based on the hosts file.
// The file is expected to contain whitespace-separated "<ip> <cores>" lines; parsing
// stops at EOF or the first malformed line (mirroring the original C fscanf-based
// loop, which also stops on the first non-matching read). A missing file is treated
// as zero cores/nodes rather than an error.
//
// Parameters:
//   - hostsPath (string): path to the hosts file listing "<ip> <cores>" per line.
//
// Returns:
//   - int: total cores summed across all successfully parsed lines.
//   - int: total number of successfully parsed lines (nodes).
//   - error: non-nil if the file exists but cannot be opened for a reason other than not existing.
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
