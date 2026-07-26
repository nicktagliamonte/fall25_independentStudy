// Purpose: P2P tuple space implementation for regex/wildcard matching and administrative tasks.
// Per planTwo 6.2: P2P tuple space is for application management (permissioned).
// Supports O(log_20 k) hops with O(N) messaging. Used for KYC, administrative coordination.

package tuplespace

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

// Constants from synergy.h. TSH_OP_* are wire opcodes sent as the first field
// of a request to the TSH daemon; FAILURE/SUCCESS are the status values used
// in TSH response structs; NAME_LEN2/TUPLENAME_LEN are the fixed-size buffer
// lengths (in bytes) for the AppId and Name/Expr fields respectively, matching
// the C struct layout.
const (
	TSH_OP_PUT     uint16 = 401
	TSH_OP_GET     uint16 = 402
	TSH_OP_READ    uint16 = 403
	TSH_OP_UVRPut3 uint16 = 415
	FAILURE        int32  = 0
	SUCCESS        int32  = 1

	NAME_LEN2     = 64
	TUPLENAME_LEN = 128
)

// TshPutIt corresponds to tsh_put_it C struct: the request header sent to the
// TSH daemon for a put operation, including the application id, tuple name,
// priority, and the requester's return address (Host/Port) for phase-2 UVR
// (unbound variable request) callbacks.
// Note: We use explicit padding to match C struct alignment (4-byte alignment typically)
type TshPutIt struct {
	AppId    [NAME_LEN2]byte
	Name     [TUPLENAME_LEN]byte
	Priority uint16
	_        [2]byte // Padding for alignment
	Host     uint32
	Port     uint16
	_        [2]byte // Padding for alignment
	Length   uint32
	ProcId   int32
}

// TshGetIt corresponds to tsh_get_it C struct: the request header sent to the
// TSH daemon for a get/read operation, including the application id, the
// match expression (tuple name or pattern), and the requester's return
// address for asynchronous delivery.
type TshGetIt struct {
	AppId   [NAME_LEN2]byte
	Expr    [TUPLENAME_LEN]byte
	Host    uint32
	Port    uint16
	_       [2]byte // Padding for alignment (after port/before len)
	Length  uint32
	ProcId  int32
	CidPort uint16
	_       [2]byte // Final padding to 212 bytes
}

// TshPutOt corresponds to tsh_put_ot C struct: the immediate status/error
// response returned by the TSH daemon after a put request.
type TshPutOt struct {
	Status int32
	Error  int32
}

// TshGetOt1 corresponds to tsh_get_ot1 C struct (same as TshPutOt): the
// immediate status/error response returned by the TSH daemon after a
// get/read request, indicating whether the tuple was available immediately
// or the caller must wait on its listener.
type TshGetOt1 struct {
	Status int32
	Error  int32
}

// TshGetOt2 corresponds to tsh_get_ot2 C struct: the tuple metadata header
// (application id, name, length, priority) that precedes the tuple payload
// bytes in a get/read response.
type TshGetOt2 struct {
	AppId    [NAME_LEN2]byte
	Name     [TUPLENAME_LEN]byte
	Length   uint32
	Priority uint16
	_        [2]byte // Padding
}

// UvrReturnStruct corresponds to tsh_put3_it C struct: a callback message
// received on the requester's temporary listener during the UVR (unbound
// variable request) wait loop of TsPut, carrying the matched waiter's address
// and whether traversal found another match (Status) and what operation it
// is waiting on (Request).
type UvrReturnStruct struct {
	Host    uint32
	Port    uint16
	_       [2]byte // Padding for alignment
	Request int32
	Status  int32
}

// P2PTupleSpace implements TupleSpace using P2P tuple space handler (TSH) daemon.
// Application management layer: permissioned; requires PermissionChecker.
// Supports regex matching at O(log_20 k) hops, O(N) messaging.
// Used for KYC, administrative coordination, and non-exact-match operations.
type P2PTupleSpace struct {
	// TshAddr is the "host:port" address of the TSH (tuple space handler) daemon.
	TshAddr string // "host:port" of the TSH daemon
	// HostIP is the local IPv4 address (as a big-endian-encoded uint32) that
	// this client reports to the TSH daemon so it can call back with UVR
	// notifications or deliver tuple data.
	HostIP uint32 // Local IP address to report to TSH (ipv4 as int)
	// AppId identifies the calling application to the TSH daemon.
	AppId string
	// PermissionChecker, if set, is consulted before TsPut/TsGet/TsRead to
	// authorize the operation. Nil means no permission check is performed.
	PermissionChecker PermissionChecker
}

// NewP2PTupleSpace creates a P2P tuple space client.
//
// Parameters:
//   - tshAddr (string): "host:port" address of the TSH daemon.
//   - hostIP (uint32): local IPv4 address to report to the TSH daemon.
//   - appId (string): application identifier reported to the TSH daemon.
//
// Returns:
//   - *P2PTupleSpace: the constructed client, with no PermissionChecker set.
func NewP2PTupleSpace(tshAddr string, hostIP uint32, appId string) *P2PTupleSpace {
	return &P2PTupleSpace{
		TshAddr: tshAddr,
		HostIP:  hostIP,
		AppId:   appId,
	}
}

// SetPermissionChecker sets the permission checker for TsPut, TsGet, TsRead.
//
// Parameters:
//   - c (PermissionChecker): the checker to install; pass nil to disable permission checks.
func (p *P2PTupleSpace) SetPermissionChecker(c PermissionChecker) {
	p.PermissionChecker = c
}

// getTempListener opens a TCP listener on an OS-assigned ephemeral port, used
// as the local return address a request registers with the TSH daemon so it
// can deliver asynchronous responses (UVR callbacks, deferred get/read data).
//
// Returns:
//   - net.Listener: the bound listener; caller is responsible for closing it.
//   - int: the local port number the listener is bound to.
//   - error: non-nil if binding the listener or parsing its assigned port failed.
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

// writeTshPutIt writes a TshPutIt's fields manually (rather than via a single
// binary.Write on the struct) so the wire layout precisely matches the C
// struct's padding/alignment, field by field, in big-endian byte order.
//
// Parameters:
//   - w (io.Writer): destination to write the encoded struct to (typically a net.Conn).
//   - s (TshPutIt): the struct to encode.
//
// Returns:
//   - error: always nil; retained for signature symmetry with other write helpers
//     (individual binary.Write errors inside are not checked/propagated).
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

// writeTshGetIt writes a TshGetIt's fields manually (rather than via a single
// binary.Write on the struct) so the wire layout precisely matches the C
// struct's padding/alignment, field by field, in big-endian byte order.
//
// Parameters:
//   - w (io.Writer): destination to write the encoded struct to (typically a net.Conn).
//   - s (TshGetIt): the struct to encode.
//
// Returns:
//   - error: always nil; retained for signature symmetry with other write helpers
//     (individual binary.Write errors inside are not checked/propagated).
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

// TsPut implements the tuple space put operation against the TSH daemon,
// following the legacy 3-phase TSH protocol: (1) send the put request and
// read an immediate status — if not FAILURE, the tuple was stored or
// consumed locally and the call returns immediately; (2) otherwise enter a
// UVR (unbound variable request) wait loop, accepting callback connections on
// a temporary listener and delivering the tuple directly to each waiter in
// turn until traversal signals no more waiters; (3) if no waiter consumed the
// tuple, store it permanently via a final TSH_OP_UVRPut3 request.
// Returns status/error code
//
// Parameters:
//   - tpname (string): the tuple name to store under.
//   - tpvalue ([]byte): the tuple payload.
//
// Returns:
//   - int: TSH-reported status/error code (0 on success, including phase 2's
//     consumed-by-GET path; TSPUT_ER on failure).
//   - error: non-nil if the permission check, listener setup, TSH connection,
//     or any protocol read/write step failed.
func (p *P2PTupleSpace) TsPut(tpname string, tpvalue []byte) (int, error) {
	if p.PermissionChecker != nil {
		if err := p.PermissionChecker.CheckPermission(OpTsPut); err != nil {
			return TSPUT_ER, err
		}
	}
	tpsize := uint32(len(tpvalue))

	// 1. Setup local listener for return connection (Phase 2)
	listener, localPort, err := getTempListener()
	if err != nil {
		return TSPUT_ER, err
	}
	defer listener.Close()

	// 2. Connect to TSH (Phase 1)
	conn, err := net.Dial("tcp", p.TshAddr)
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
	copy(out.AppId[:], []byte(p.AppId))
	copy(out.Name[:], []byte(tpname))
	out.Priority = 1 // Saved for later implementation
	out.Length = tpsize
	out.Host = p.HostIP
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
	conn3, err := net.Dial("tcp", p.TshAddr)
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

// TsGet implements the tuple space get operation (consuming) against the TSH
// daemon: sends a get request and, if not immediately available, waits on a
// temporary listener for the daemon (or a matching TsPut waiter) to deliver
// the tuple metadata and payload asynchronously. The returned tuple name is
// validated against tpname when tpname is a literal (non-pattern) expression.
// Returns the data in a newly allocated slice (like C tsgetv)
// To mimic C tsget (buffer provided), one could use a different signature, but this is safer.
//
// Parameters:
//   - tpname (string): the tuple name or match expression to consume.
//
// Returns:
//   - []byte: the consumed tuple's payload, newly allocated.
//   - error: non-nil if the permission check, listener setup, TSH connection,
//     any protocol read/write step failed, or (for literal patterns) the
//     returned tuple's name does not match tpname.
func (p *P2PTupleSpace) TsGet(tpname string) ([]byte, error) {
	if p.PermissionChecker != nil {
		if err := p.PermissionChecker.CheckPermission(OpTsGet); err != nil {
			return nil, err
		}
	}
	// 1. Setup local listener just in case we need to wait
	listener, localPort, err := getTempListener()
	if err != nil {
		return nil, fmt.Errorf("get_socket: %w", err)
	}
	defer listener.Close()

	// 2. Connect to TSH
	conn, err := net.Dial("tcp", p.TshAddr)
	if err != nil {
		return nil, fmt.Errorf("connectTsh: %w", err)
	}

	opCode := TSH_OP_GET
	if err := binary.Write(conn, binary.BigEndian, opCode); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tsget: Op code send error: %w", err)
	}

	var out TshGetIt
	copy(out.AppId[:], []byte(p.AppId))
	copy(out.Expr[:], []byte(tpname))
	out.Host = p.HostIP
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
	returnedName := strings.TrimRight(string(in2.Name[:]), "\x00")
	if !strings.ContainsAny(tpname, "*?[]+().\\^$|") && returnedName != tpname {
		return nil, fmt.Errorf("tsget: returned tuple name %q does not match pattern %q", returnedName, tpname)
	}

	// Read Data
	data := make([]byte, actualSize)
	if _, err := io.ReadFull(dataSocket, data); err != nil {
		return nil, fmt.Errorf("tsget: tuple read error: %w", err)
	}

	return data, nil
}

// TsRead implements the tuple space read operation (non-consuming) against
// the TSH daemon: sends a read request and, if not immediately available,
// waits on a temporary listener for the daemon to deliver the tuple metadata
// and payload asynchronously. Unlike TsGet, the returned tuple name is not
// validated against tpname.
//
// Parameters:
//   - tpname (string): the tuple name or match expression to read.
//
// Returns:
//   - []byte: the matched tuple's payload, newly allocated.
//   - error: non-nil if the permission check, listener setup, TSH connection,
//     or any protocol read/write step failed.
func (p *P2PTupleSpace) TsRead(tpname string) ([]byte, error) {
	if p.PermissionChecker != nil {
		if err := p.PermissionChecker.CheckPermission(OpTsRead); err != nil {
			return nil, err
		}
	}
	// 1. Setup local listener just in case we need to wait
	listener, localPort, err := getTempListener()
	if err != nil {
		return nil, fmt.Errorf("get_socket: %w", err)
	}
	defer listener.Close()

	// 2. Connect to TSH
	conn, err := net.Dial("tcp", p.TshAddr)
	if err != nil {
		return nil, fmt.Errorf("connectTsh: %w", err)
	}

	opCode := TSH_OP_READ
	if err := binary.Write(conn, binary.BigEndian, opCode); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tsread: Op code send error: %w", err)
	}

	var out TshGetIt
	copy(out.AppId[:], []byte(p.AppId))
	copy(out.Expr[:], []byte(tpname))
	out.Host = p.HostIP
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
