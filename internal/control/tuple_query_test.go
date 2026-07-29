package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mygateway "github.com/nicktagliamonte/fall25_independentStudy/internal/gateway"
	mytuplespace "github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

type instrumentedTupleSpaceStub struct {
	value []byte
	err   error
	puts  int
}

func (s *instrumentedTupleSpaceStub) TsPut(string, []byte) (int, error) {
	s.puts++
	return 0, nil
}

func (s *instrumentedTupleSpaceStub) TsPutWithMutationStats(name string, value []byte) (int, mytuplespace.IndexMutationStats, error) {
	code, err := s.TsPut(name, value)
	return code, mytuplespace.IndexMutationStats{
		Total: 1, Local: 1, PerShard: []uint64{1, 0},
	}, err
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
	return mytuplespace.IndexMutationStats{Total: uint64(4 + s.puts), Local: uint64(s.puts), PerShard: []uint64{uint64(1 + s.puts), 3}}
}

func TestTupleQueryEndpointReturnsInstrumentation(t *testing.T) {
	mux := http.NewServeMux()
	ts := &instrumentedTupleSpaceStub{value: []byte("token")}
	registerTupleExperimentEndpoints(mux, mygateway.NewGateway(nil, ts))

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
	registerTupleExperimentEndpoints(mux, nil)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/tuple/query", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestTuplePutEndpointPopulatesWorkload(t *testing.T) {
	mux := http.NewServeMux()
	ts := &instrumentedTupleSpaceStub{}
	registerTupleExperimentEndpoints(mux, mygateway.NewGateway(nil, ts))

	body := strings.NewReader(`{"names":["experiment/run-1","experiment/run-2"],"value_base64":"dG9rZW4=","copies":3,"concurrency":2}`)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/tuple/put", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if ts.puts != 6 {
		t.Fatalf("puts = %d, want 6", ts.puts)
	}
	var response tuplePutResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.MutationDelta.Total != 6 || response.MutationDelta.Failures != 0 {
		t.Fatalf("unexpected mutation delta: %+v", response.MutationDelta)
	}
}
