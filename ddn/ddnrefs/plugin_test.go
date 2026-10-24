// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package ddnrefs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/the-protobuf-project/store/ddn/ddnrefs"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const cardType = "vcard.example.com/Card"

// config is what protoc-gen-store emits for the references golden case.
var config = ddnrefs.Config{
	Version: ddnrefs.Version,
	Mutations: []ddnrefs.Mutation{
		{Field: "insertLendingLoans", Kind: "insert", ResourceType: "lending.example.com/Loan", References: []ddnrefs.Reference{
			{Field: "borrower", Column: "borrower", Type: cardType, OnDelete: "ON_DELETE_RESTRICT"},
		}},
		{Field: "updateLendingLoansById", Kind: "update", ResourceType: "lending.example.com/Loan", References: []ddnrefs.Reference{
			{Field: "borrower", Column: "borrower", Type: cardType, OnDelete: "ON_DELETE_RESTRICT"},
		}},
		{Field: "deleteCatalogShelvesById", Kind: "delete", ResourceType: "catalog.example.com/Shelf", Pattern: "shelves/{shelf}", KeyArgument: "keyId"},
		{Field: "deleteCatalogBooksById", Kind: "delete", ResourceType: "catalog.example.com/Book", Pattern: "shelves/{shelf}/books/{book}", KeyArgument: "keyId", ByIDQuery: "catalogBooksById"},
	},
	Endpoints: []ddnrefs.Endpoint{
		{Type: cardType, Get: "vcard.v1.Cards/GetCard", Target: "${CARDS}", Referrers: []string{"catalog.example.com/Shelf", "catalog.example.com/Book"}},
	},
}

// referrers is a fake ReferenceService: shelves/s1 is RESTRICTed, books/b1
// on shelf s2 is NOTIFYed.
type referrers struct {
	storepbv1.UnimplementedReferenceServiceServer
}

func (referrers) ListReferrers(_ context.Context, req *storepbv1.ListReferrersRequest) (*storepbv1.ListReferrersResponse, error) {
	switch req.GetParent() {
	case "shelves/s1":
		return &storepbv1.ListReferrersResponse{Referrers: []*storepbv1.Referrer{{Referrer: "orders/1", ResourceType: "shop.example.com/Order", Field: "shelf", OnDelete: storepbv1.ReferenceOptions_ON_DELETE_RESTRICT}}}, nil
	case "shelves/s2/books/b1":
		return &storepbv1.ListReferrersResponse{Referrers: []*storepbv1.Referrer{{Referrer: "orders/2", ResourceType: "shop.example.com/Order", Field: "item", OnDelete: storepbv1.ReferenceOptions_ON_DELETE_NOTIFY}}}, nil
	}
	return &storepbv1.ListReferrersResponse{}, nil
}

func getCard(_ any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	req := new(wrapperspb.StringValue)
	if err := dec(req); err != nil {
		return nil, err
	}
	if req.GetValue() != "cards/1" {
		return nil, status.Error(codes.NotFound, "no such card")
	}
	return &emptypb.Empty{}, nil
}

func newPlugin(t *testing.T) (*ddnrefs.Plugin, *[]map[string]any) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "vcard.v1.Cards", HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{MethodName: "GetCard", Handler: getCard}},
	}, struct{}{})
	storepbv1.RegisterReferenceServiceServer(srv, referrers{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	// DDN answers the by-key read of a nested resource's name.
	ddn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("x-hasura-admin-secret") != "admin" || !strings.Contains(body.Query, `catalogBooksById(id: "B1")`) {
			_, _ = io.WriteString(w, `{"data":{"r":null}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"r":{"name":"shelves/s2/books/b1"}}}`)
	}))
	t.Cleanup(ddn.Close)

	var mu sync.Mutex
	events := &[]map[string]any{}
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e map[string]any
		_ = json.NewDecoder(r.Body).Decode(&e)
		if r.Header.Get("Content-Type") == "application/cloudevents+json" {
			mu.Lock()
			*events = append(*events, e)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(sink.Close)

	cfg := config
	p, err := ddnrefs.New(&cfg, ddnrefs.Options{
		Dial: func(string) (grpc.ClientConnInterface, error) {
			return grpc.NewClient("passthrough:///bufnet",
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
				grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
		DDNURL:     ddn.URL,
		DDNHeaders: map[string]string{"x-hasura-admin-secret": "admin"},
		Secret:     "s3cret",
		Sink:       sink.URL,
		Source:     "//ddn",
		Getenv:     func(string) string { return "bufnet" },
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, events
}

// call sends one hook request and returns the status and body.
func call(t *testing.T, h http.Handler, body map[string]any, secret string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("hasura-m-auth", secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func parse(query string, vars map[string]any) map[string]any {
	return map[string]any{
		"rawRequest": map[string]any{"query": query, "variables": vars},
		"session":    map[string]any{"role": "admin"},
	}
}

func TestPreParse(t *testing.T) {
	p, _ := newPlugin(t)
	h := p.PreParse()
	for _, tc := range []struct {
		name   string
		query  string
		vars   map[string]any
		code   int
		reason string
	}{
		{"query passes", `query { lendingLoans { id } }`, nil, 204, ""},
		{"insert, card exists", `mutation { insertLendingLoans(objects: [{id: "L1", borrower: "cards/1"}]) { affectedRows } }`, nil, 204, ""},
		{"insert, card missing", `mutation { insertLendingLoans(objects: [{id: "L1", borrower: "cards/9"}]) { affectedRows } }`, nil, 400, "REFERENCE_NOT_FOUND"},
		{"insert through variables", `mutation M($o: [InsertLendingLoansObject!]!) { insertLendingLoans(objects: $o) { affectedRows } }`,
			map[string]any{"o": []any{map[string]any{"borrower": "cards/9"}}}, 400, "REFERENCE_NOT_FOUND"},
		{"update set", `mutation { updateLendingLoansById(keyId: "L1", updateColumns: {borrower: {set: "cards/9"}}) { affectedRows } }`, nil, 400, "REFERENCE_NOT_FOUND"},
		{"update preCheck is not a write", `mutation { updateLendingLoansById(keyId: "L1", preCheck: {borrower: {_eq: "cards/9"}}, updateColumns: {state: {set: "x"}}) { affectedRows } }`, nil, 204, ""},
		{"delete restricted", `mutation { deleteCatalogShelvesById(keyId: "s1") { affectedRows } }`, nil, 400, "RESOURCE_REFERENCED"},
		{"delete unreferenced", `mutation { deleteCatalogShelvesById(keyId: "s3") { affectedRows } }`, nil, 204, ""},
		{"aliased, in a fragment", `mutation { ...F } fragment F on Mutation { x: insertLendingLoans(objects: {borrower: "cards/9"}) { affectedRows } }`, nil, 400, "REFERENCE_NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := call(t, h, parse(tc.query, tc.vars), "s3cret")
			if code != tc.code {
				t.Fatalf("status %d, want %d: %v", code, tc.code, body)
			}
			if tc.reason != "" {
				ext, _ := body["extensions"].(map[string]any)
				if ext["reason"] != tc.reason || ext["code"] != "FAILED_PRECONDITION" || body["message"] == "" {
					t.Fatalf("body %v", body)
				}
			}
		})
	}
	if code, _ := call(t, h, parse(`mutation { x }`, nil), "wrong"); code != 500 {
		t.Fatalf("a call without the plugin secret: %d", code)
	}
}

func TestNotifyAfterDelete(t *testing.T) {
	p, events := newPlugin(t)
	q := `mutation { gone: deleteCatalogBooksById(keyId: "B1") { affectedRows } }`
	if code, body := call(t, p.PreParse(), parse(q, nil), "s3cret"); code != 204 {
		t.Fatalf("pre-parse %d %v", code, body)
	}
	resp := parse(q, nil)
	resp["response"] = map[string]any{"data": map[string]any{"gone": map[string]any{"affectedRows": 1}}}
	if code, _ := call(t, p.PreResponse(), resp, "s3cret"); code != 204 {
		t.Fatalf("pre-response %d", code)
	}
	if len(*events) != 1 {
		t.Fatalf("events %v", *events)
	}
	e := (*events)[0]
	data, _ := e["data"].(map[string]any)
	if e["type"] != ddnrefs.EventType || e["subject"] != "shelves/s2/books/b1" || data["target"] != "shelves/s2/books/b1" {
		t.Fatalf("event %v", e)
	}
	// A delete that removed nothing sends nothing.
	call(t, p.PreParse(), parse(q, nil), "s3cret")
	resp["response"] = map[string]any{"data": map[string]any{"gone": map[string]any{"affectedRows": 0}}}
	call(t, p.PreResponse(), resp, "s3cret")
	if len(*events) != 1 {
		t.Fatalf("events %v", *events)
	}
}
