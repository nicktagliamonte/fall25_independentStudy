package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mygateway "github.com/nicktagliamonte/fall25_independentStudy/internal/gateway"
	mytuplespace "github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

type instrumentedTupleSpaceStub struct {
	value []byte
	err   error
}

func (s *instrumentedTupleSpaceStub) TsPut(string, []byte) (int, error) {
	return 0, nil
}

func (s *instrumentedTupleSpaceStub) TsRead(string) ([]byte, error) {
	return s.value, s.err
}

func (s *instrumentedTupleSpaceStub) TsGet(string) ([]byte, error) {
	return s.value, s.err
}

func (s *instrumentedTupleSpaceStub) TsReadWithStats(string) ([]byte, mytuplespace.IndexedQueryStats, error) {
	return s.value, mytuplespace.IndexedQueryStats{
		QueryKind:       "prefix",
		ShardsContacted: 16,
		NodesFetched:    7,
		IndexCandidates: 3,
		VerifiedMatches: 1,
	}, s.err
}

func (s *instrumentedTupleSpaceStub) MutationSnapshot() mytuplespace.IndexMutationStats {
	return mytuplespace.IndexMutationStats{Total: 4, PerShard: []uint64{1, 3}}
}

func TestTupleQueryEndpointReturnsInstrumentation(t *testing.T) {
	mux := http.NewServeMux()
	ts := &instrumentedTupleSpaceStub{value: []byte("token")}
	registerTupleQueryEndpoint(mux, mygateway.NewGateway(nil, ts))

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/tuple/query?pattern=data%2F*", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response tupleQueryResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Pattern != "data/*" || response.ValueBase64 != "dG9rZW4=" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.QueryStats.ShardsContacted != 16 || response.QueryStats.NodesFetched != 7 {
		t.Fatalf("unexpected query stats: %+v", response.QueryStats)
	}
	if response.MutationStats.Total != 4 {
		t.Fatalf("unexpected mutation stats: %+v", response.MutationStats)
	}
}

func TestTupleQueryEndpointValidatesRequest(t *testing.T) {
	mux := http.NewServeMux()
	registerTupleQueryEndpoint(mux, nil)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/tuple/query", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
