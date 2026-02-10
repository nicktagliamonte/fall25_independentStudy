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

// Constants from synergy.h
const (
	TSH_OP_PUT     uint16 = 401
	TSH_OP_GET     uint16 = 402
	TSH_OP_READ    uint16 = 403
	TSH_OP_UVRPut3 uint16 = 415
	FAILURE        int32  = 0
	SUCCESS        int32  = 1
	TSPUT_ER       int    = -106
	TSGET_ER       int    = -107
	TSREAD_ER      int    = -108

	NAME_LEN2     = 64
	TUPLENAME_LEN = 128
)

// TshPutIt corresponds to tsh_put_it C struct
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

// TshGetIt corresponds to tsh_get_it C struct
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

// TshPutOt corresponds to tsh_put_ot C struct
type TshPutOt struct {
	Status int32
	Error  int32
}

// TshGetOt1 corresponds to tsh_get_ot1 C struct (same as TshPutOt)
type TshGetOt1 struct {
	Status int32
	Error  int32
}

// TshGetOt2 corresponds to tsh_get_ot2 C struct
type TshGetOt2 struct {
	AppId    [NAME_LEN2]byte
	Name     [TUPLENAME_LEN]byte
	Length   uint32
	Priority uint16
	_        [2]byte // Padding
}

// UvrReturnStruct corresponds to tsh_put3_it C struct
type UvrReturnStruct struct {
	Host    uint32
	Port    uint16
	_       [2]byte // Padding for alignment
	Request int32
	Status  int32
}

// TupleSpaceClient holds configuration for the tuple space operations
type TupleSpaceClient struct {
	TshAddr string // "host:port" of the TSH daemon
	HostIP  uint32 // Local IP address to report to TSH (ipv4 as int)
	AppId   string
}

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

// writeStructManual writes struct fields manually to handle padding correctly
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

// TsPut implements the tuple space put operation
// Returns status/error code
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

// TsGet implements the tuple space get operation (consuming)
// Returns the data in a newly allocated slice (like C tsgetv)
// To mimic C tsget (buffer provided), one could use a different signature, but this is safer.
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

// TsRead implements the tuple space read operation (non-consuming)
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

// UvrCores calculates the total number of cores deployed at runtime based on the hosts file
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
