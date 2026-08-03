package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	ds "github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"

	mygateway "github.com/nicktagliamonte/fall25_independentStudy/internal/gateway"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/names"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
	mytuplespace "github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

type signedNameRequest struct {
	ExpectedGeneration uint64            `json:"expected_generation"`
	Record             *names.NameRecord `json:"record,omitempty"`
	RecordCBOR         []byte            `json:"record_cbor,omitempty"`
}

type signedLeaseRequest struct {
	Lease     *names.LeaseRecord `json:"lease,omitempty"`
	LeaseCBOR []byte             `json:"lease_cbor,omitempty"`
}

func (request signedNameRequest) canonicalRecord() ([]byte, error) {
	if len(request.RecordCBOR) != 0 {
		if _, err := names.DecodeNameRecord(request.RecordCBOR); err != nil {
			return nil, err
		}
		return request.RecordCBOR, nil
	}
	if request.Record == nil {
		return nil, errors.New("record or record_cbor is required")
	}
	return request.Record.Marshal()
}

func (request signedLeaseRequest) canonicalLease() ([]byte, error) {
	if len(request.LeaseCBOR) != 0 {
		var lease names.LeaseRecord
		if err := names.UnmarshalCanonical(request.LeaseCBOR, &lease); err != nil {
			return nil, err
		}
		return request.LeaseCBOR, nil
	}
	if request.Lease == nil {
		return nil, errors.New("lease or lease_cbor is required")
	}
	return names.MarshalCanonical(request.Lease)
}

func registerNamedObjectHandlers(mux *http.ServeMux, stack *mystore.Stack, h host.Host, repair *mystore.RepairProtocol, gateway *mygateway.Gateway) {
	if stack == nil || stack.Datastore == nil {
		return
	}
	var network routing.ValueStore
	if stack.DHT != nil {
		network = stack.DHT
	}
	publicationGate := strictPublicationGate(stack, h, repair)
	service := names.NewService(stack.Datastore, network, publicationGate)
	service.SetCommitHook(namedPolicyCommitHook(stack, repair))
	if gateway != nil && gateway.TupleSpace != nil {
		if logical, ok := gateway.TupleSpace.(logicalNamePHT); ok {
			service.SetSearchIndex(logicalNameIndexAdapter{logical})
		}
		if authority, ok := gateway.TupleSpace.(mytuplespace.ExactCompareAndSwapper); ok {
			service.SetAuthority(exactNameAuthorityAdapter{authority})
		}
	}

	mux.HandleFunc("/v1/names/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		attempted := queryInt(r, "fanout_attempted", 1)
		completed := queryInt(r, "fanout_completed", attempted)
		result, err := service.Search(r.Context(), r.URL.Query().Get("prefix"), r.URL.Query().Get("suffix"), attempted, completed)
		if err != nil {
			writeNamedError(w, err)
			return
		}
		writeNamedJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("/v1/names/preflight", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		var request signedNameRequest
		if err := decodeBoundedJSON(r, &request); err != nil {
			writeNamedError(w, err)
			return
		}
		raw, err := request.canonicalRecord()
		if err != nil {
			writeNamedError(w, err)
			return
		}
		record, err := names.DecodeNameRecord(raw)
		if err == nil {
			err = record.ValidateEnvelope(time.Now())
		}
		if err != nil {
			writeNamedError(w, err)
			return
		}
		if !record.Policy.StrictPublish {
			writeNamedJSON(w, http.StatusOK, map[string]any{"ready": true})
			return
		}
		if err := publicationGate(r.Context(), record); err != nil {
			writeNamedJSON(w, http.StatusOK, map[string]any{"ready": false, "detail": err.Error()})
			return
		}
		writeNamedJSON(w, http.StatusOK, map[string]any{"ready": true})
	})

	mux.HandleFunc("/v1/names", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		var request signedNameRequest
		if err := decodeBoundedJSON(r, &request); err != nil {
			writeNamedError(w, err)
			return
		}
		raw, err := request.canonicalRecord()
		if err != nil {
			writeNamedError(w, err)
			return
		}
		record, err := service.Create(r.Context(), raw)
		if err != nil {
			writeNamedError(w, err)
			return
		}
		writeNamedJSON(w, http.StatusCreated, map[string]any{"name_id": fmt.Sprintf("%x", record.NameID), "record": record, "record_cbor": raw})
	})

	mux.HandleFunc("/v1/names/", func(w http.ResponseWriter, r *http.Request) {
		idText := strings.TrimPrefix(r.URL.Path, "/v1/names/")
		if idText == "" || strings.Contains(idText, "/") {
			http.NotFound(w, r)
			return
		}
		id, err := names.ParseNameID(idText)
		if err != nil {
			writeNamedError(w, err)
			return
		}
		switch r.Method {
		case http.MethodGet:
			record, raw, err := service.Get(r.Context(), id)
			if err != nil {
				writeNamedError(w, err)
				return
			}
			writeNamedJSON(w, http.StatusOK, map[string]any{"record": record, "record_cbor": raw})
		case http.MethodPut, http.MethodDelete:
			var request signedNameRequest
			if err := decodeBoundedJSON(r, &request); err != nil {
				writeNamedError(w, err)
				return
			}
			raw, err := request.canonicalRecord()
			if err != nil {
				writeNamedError(w, err)
				return
			}
			var record *names.NameRecord
			if r.Method == http.MethodDelete {
				record, err = service.Delete(r.Context(), id, request.ExpectedGeneration, raw)
			} else {
				record, err = service.Update(r.Context(), id, request.ExpectedGeneration, raw)
			}
			if err != nil {
				writeNamedError(w, err)
				return
			}
			writeNamedJSON(w, http.StatusOK, map[string]any{"record": record, "record_cbor": raw})
		default:
			w.Header().Set("Allow", "GET, PUT, DELETE")
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	})

	registerLeaseEndpoint := func(route, operation string) {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				methodNotAllowed(w, http.MethodPost)
				return
			}
			var request signedLeaseRequest
			if err := decodeBoundedJSON(r, &request); err != nil {
				writeNamedError(w, err)
				return
			}
			raw, err := request.canonicalLease()
			if err != nil {
				writeNamedError(w, err)
				return
			}
			switch operation {
			case "acquire":
				lease, err := service.AcquireLease(r.Context(), raw)
				if err != nil {
					writeNamedError(w, err)
					return
				}
				writeNamedJSON(w, http.StatusCreated, lease)
			case "renew":
				lease, err := service.RenewLease(r.Context(), raw)
				if err != nil {
					writeNamedError(w, err)
					return
				}
				writeNamedJSON(w, http.StatusOK, lease)
			case "release":
				if err := service.ReleaseLease(r.Context(), raw); err != nil {
					writeNamedError(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}
		})
	}
	registerLeaseEndpoint("/v1/locks/acquire", "acquire")
	registerLeaseEndpoint("/v1/locks/renew", "renew")
	registerLeaseEndpoint("/v1/locks/release", "release")
}

func namedPolicyCommitHook(stack *mystore.Stack, repair *mystore.RepairProtocol) func(*names.NameRecord) {
	return func(record *names.NameRecord) {
		if record.Tombstone {
			marker, _ := json.Marshal(map[string]any{"name_id": fmt.Sprintf("%x", record.NameID), "tombstone_generation": record.Generation, "collect_after_ns": record.Timestamp + record.Policy.CollectionGrace, "mode": "best-effort"})
			_ = stack.Datastore.Put(context.Background(), ds.NewKey("/mutable/gc/"+fmt.Sprintf("%x", record.NameID)), marker)
			return
		}
		if repair == nil {
			return
		}
		copyRecord := *record
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			if err := repairNamedObject(ctx, stack, repair, &copyRecord); err != nil {
				log.Printf("named-object policy repair %x generation %d: %v", copyRecord.NameID, copyRecord.Generation, err)
			}
		}()
	}
}

func repairNamedObject(ctx context.Context, stack *mystore.Stack, repair *mystore.RepairProtocol, record *names.NameRecord) error {
	var manifestKey mystore.Key
	copy(manifestKey[:], record.ManifestKey)
	manifestRaw, err := mystore.GetBlockByKey(ctx, stack.Datastore, stack.BlockSvc, manifestKey)
	if err != nil {
		return err
	}
	manifest, err := names.DecodeObjectManifest(manifestRaw)
	if err != nil {
		return err
	}
	keys := []mystore.Key{manifestKey}
	for _, ref := range manifest.Chunks {
		var key mystore.Key
		copy(key[:], ref.CiphertextKey)
		keys = append(keys, key)
	}
	vector := mystore.ReplicationVector{Near: float64(record.Policy.Placement.Near) / float64(record.Policy.Replicas), Midrange: float64(record.Policy.Placement.Middle) / float64(record.Policy.Replicas), FarFlung: float64(record.Policy.Placement.Far) / float64(record.Policy.Replicas)}
	for _, key := range keys {
		if stack.RoutingTable != nil {
			stack.RoutingTable.UpdateRepVector(key, vector)
		}
		if _, _, err := repair.AuditAndRepair(ctx, key, int(record.Policy.Replicas)); err != nil {
			return err
		}
	}
	return nil
}

type logicalNamePHT interface {
	IndexLogicalName(context.Context, string) error
	DeleteLogicalName(context.Context, string) error
	SearchLogicalNames(context.Context, string, string) ([]string, int, int, error)
}

type logicalNameIndexAdapter struct{ logicalNamePHT }

func (a logicalNameIndexAdapter) Insert(ctx context.Context, entry string) error {
	return a.IndexLogicalName(ctx, entry)
}

type exactNameAuthorityAdapter struct {
	mytuplespace.ExactCompareAndSwapper
}

func (a exactNameAuthorityAdapter) Read(ctx context.Context, name string) ([]byte, error) {
	value, err := a.ReadExact(ctx, name)
	if errors.Is(err, mytuplespace.ErrTupleNotFound) {
		return nil, names.ErrNotFound
	}
	return value, err
}
func (a exactNameAuthorityAdapter) CompareAndSwap(ctx context.Context, name string, expected, next []byte) error {
	err := a.CompareAndSwapExact(ctx, name, expected, next)
	if errors.Is(err, mytuplespace.ErrTupleCASConflict) {
		return names.ErrConflict
	}
	return err
}
func (a logicalNameIndexAdapter) Delete(ctx context.Context, entry string) error {
	return a.DeleteLogicalName(ctx, entry)
}
func (a logicalNameIndexAdapter) Query(ctx context.Context, prefix, suffix string) ([]string, int, int, error) {
	return a.SearchLogicalNames(ctx, prefix, suffix)
}

func strictPublicationGate(stack *mystore.Stack, h host.Host, repair *mystore.RepairProtocol) names.PublicationGate {
	return func(ctx context.Context, record *names.NameRecord) error {
		if record.Tombstone {
			return nil
		}
		var manifestKey mystore.Key
		copy(manifestKey[:], record.ManifestKey)
		manifestRaw, err := mystore.GetBlockByKey(ctx, stack.Datastore, stack.BlockSvc, manifestKey)
		if err != nil || len(manifestRaw) == 0 {
			return errors.New("manifest is not staged locally")
		}
		computedManifestKey := mystore.KeyFromData(manifestRaw)
		if !bytes.Equal(computedManifestKey[:], record.ManifestKey) {
			return errors.New("manifest content key mismatch")
		}
		manifest, err := names.DecodeObjectManifest(manifestRaw)
		if err != nil {
			return err
		}
		if !bytes.Equal(manifest.Signer, record.Owner) {
			return errors.New("manifest signer is not namespace owner")
		}
		keys := make([]mystore.Key, 0, len(manifest.Chunks)+1)
		keys = append(keys, manifestKey)
		for _, chunk := range manifest.Chunks {
			var key mystore.Key
			copy(key[:], chunk.CiphertextKey)
			keys = append(keys, key)
		}
		providerID := peer.ID("")
		if h != nil {
			providerID = h.ID()
		}
		var measurer mystore.ProviderRTTMeasurer
		if repair != nil {
			measurer = repair.MeasureRTTAt
		}
		for _, key := range keys {
			counts, err := signedProviderCounts(ctx, stack, key, providerID, measurer)
			if err != nil {
				return err
			}
			if counts.total < int(record.Policy.Replicas) || counts.near < int(record.Policy.Placement.Near) || counts.middle < int(record.Policy.Placement.Middle) || counts.far < int(record.Policy.Placement.Far) {
				return fmt.Errorf("key %s has signed verified placement %d/%d/%d total=%d; requires %d/%d/%d total=%d", key.String(), counts.near, counts.middle, counts.far, counts.total, record.Policy.Placement.Near, record.Policy.Placement.Middle, record.Policy.Placement.Far, record.Policy.Replicas)
			}
		}
		return nil
	}
}

type providerCounts struct{ near, middle, far, total int }

func signedProviderCounts(ctx context.Context, stack *mystore.Stack, key mystore.Key, local peer.ID, measurer mystore.ProviderRTTMeasurer) (providerCounts, error) {
	var counts providerCounts
	if stack.DHT == nil {
		return counts, errors.New("signed provider claims require DHT")
	}
	token, err := mystore.GetToken(ctx, stack.DHT, key)
	if err != nil {
		return counts, err
	}
	validator := &names.ProviderClaimValidator{}
	type verifiedProvider struct {
		category mystore.DistanceCategory
		valid    bool
	}
	results := make(chan verifiedProvider, len(token.Locations))
	for _, location := range token.Locations {
		location := location
		go func() {
			public, err := location.ProviderID.ExtractPublicKey()
			if err != nil {
				results <- verifiedProvider{}
				return
			}
			publicRaw, err := public.Raw()
			if err != nil || len(publicRaw) != 32 {
				results <- verifiedProvider{}
				return
			}
			claimKey := fmt.Sprintf("/providers/%x/%x", key[:], publicRaw)
			raw, err := stack.DHT.GetValue(ctx, claimKey)
			if err != nil || validator.Validate(claimKey, raw) != nil {
				results <- verifiedProvider{}
				return
			}
			category := mystore.DistanceUnknown
			if location.ProviderID == local {
				category = mystore.DistanceNear
			} else if measurer != nil {
				rtt, measureErr := measurer(location.ProviderID, location.Address)
				if measureErr == nil {
					category = mystore.ClassifyDistanceByRTT(rtt, nil)
				}
			} else {
				category = mystore.ClassifyDistanceByRTT(location.RTT, nil)
			}
			results <- verifiedProvider{category: category, valid: category != mystore.DistanceUnknown}
		}()
	}
	for range token.Locations {
		result := <-results
		if !result.valid {
			continue
		}
		switch result.category {
		case mystore.DistanceNear:
			counts.near++
		case mystore.DistanceMidrange:
			counts.middle++
		case mystore.DistanceFarFlung:
			counts.far++
		default:
			continue
		}
		counts.total++
	}
	return counts, nil
}

func decodeBoundedJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func queryInt(r *http.Request, name string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func methodNotAllowed(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
}

func writeNamedJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeNamedError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, names.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, names.ErrConflict), errors.Is(err, names.ErrLocked):
		status = http.StatusConflict
	case strings.Contains(err.Error(), "strict publication"):
		status = http.StatusServiceUnavailable
	}
	writeNamedJSON(w, status, map[string]string{"error": err.Error()})
}
