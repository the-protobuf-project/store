// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package references_test

// repox_test.go exercises the generated reference runtime (repox) with fakes:
// resolvers, referrer checkers, the NOTIFY event, ListReferrers paging, the
// reconciler, and the gRPC binding over an in-process connection. No
// database.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/the-protobuf-project/store/examples/generated/go/repository/references/repox"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const cardType = "vcard.example.com/Card"

// cards is a fake card service: an AIP Get by name over a set.
type cards map[string]bool

func (c cards) get(_ context.Context, name string) (string, error) {
	if !c[name] {
		return "", repox.ErrNotFound
	}
	return name, nil
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	refs := repox.NewReferences().Resolver(cardType, repox.GetResolver(cards{"cards/1": true}.get))

	if err := refs.Resolve(ctx, "borrower", cardType, "cards/1"); err != nil {
		t.Fatalf("existing card: %v", err)
	}
	err := refs.Resolve(ctx, "borrower", cardType, "cards/2")
	if !errors.Is(err, repox.ErrReferenceNotFound) {
		t.Fatalf("missing card: got %v, want ErrReferenceNotFound", err)
	}
	var re *repox.ReferenceError
	if !errors.As(err, &re) || re.Field != "borrower" || re.Name != "cards/2" || re.Reason() != repox.ReasonReferenceNotFound {
		t.Fatalf("detail: %#v", re)
	}
	if err := refs.Resolve(ctx, "borrower", cardType, ""); err != nil {
		t.Fatalf("an unset reference is not resolved: %v", err)
	}
	// No resolver for the type: the write goes ahead.
	if err := refs.Resolve(ctx, "x", "other.example.com/Thing", "things/1"); err != nil {
		t.Fatalf("no resolver: %v", err)
	}
	// A nil registry enforces nothing.
	var none *repox.References
	if err := none.Resolve(ctx, "borrower", cardType, "cards/2"); err != nil {
		t.Fatalf("nil registry: %v", err)
	}
	// A resolver that cannot tell fails the write with its own error.
	boom := errors.New("unavailable")
	refs.Resolver("flaky.example.com/X", repox.ResolverFunc(func(context.Context, string, string) error { return boom }))
	if err := refs.Resolve(ctx, "x", "flaky.example.com/X", "xs/1"); !errors.Is(err, boom) || errors.Is(err, repox.ErrReferenceNotFound) {
		t.Fatalf("unavailable resolver: %v", err)
	}
}

// fixedReferrers answers every question with the same referrers.
func fixedReferrers(rs ...repox.Referrer) repox.ReferrerChecker {
	return repox.CheckerFunc(func(context.Context, string, string) ([]repox.Referrer, error) { return rs, nil })
}

// recorder is an EventPublisher keeping what it was given.
type recorder struct {
	mu     sync.Mutex
	events []repox.CloudEvent
}

func (r *recorder) Publish(_ context.Context, e repox.CloudEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func TestCheckDelete(t *testing.T) {
	ctx := context.Background()
	restrict := repox.Referrer{Name: "loans/1", ResourceType: "lending.example.com/Loan", Field: "borrower", OnDelete: repox.OnDeleteRestrict}
	notify := repox.Referrer{Name: "loans/2", ResourceType: "lending.example.com/Loan", Field: "guarantor", OnDelete: repox.OnDeleteNotify}
	allow := repox.Referrer{Name: "loans/3", ResourceType: "lending.example.com/Loan", Field: "x", OnDelete: repox.OnDeleteAllow}

	refs := repox.NewReferences().Referrers(cardType, fixedReferrers(restrict, notify, allow))
	_, err := refs.CheckDelete(ctx, cardType, "cards/1")
	var re *repox.ReferenceError
	if !errors.Is(err, repox.ErrReferenced) || !errors.As(err, &re) || len(re.Referrers) != 1 || re.Referrers[0].Name != "loans/1" || re.Reason() != repox.ReasonReferenced {
		t.Fatalf("restrict: %v", err)
	}

	rec := &recorder{}
	refs = repox.NewReferences().Referrers(cardType, fixedReferrers(notify, allow)).Publisher("//cards", rec)
	check, err := refs.CheckDelete(ctx, cardType, "cards/1")
	if err != nil {
		t.Fatalf("notify and allow do not refuse: %v", err)
	}
	check.Done(ctx, false)
	if len(rec.events) != 1 {
		t.Fatalf("events: %d, want 1", len(rec.events))
	}
	e := rec.events[0]
	if e.SpecVersion != "1.0" || e.Type != repox.EventTypeReferenceDeleted || e.Subject != "cards/1" || e.Source != "//cards" || e.ID == "" {
		t.Fatalf("event attributes: %+v", e)
	}
	// The data is store.v1.ReferencedResourceDeleted in proto3 JSON.
	var data storepbv1.ReferencedResourceDeleted
	if err := protojson.Unmarshal(e.Data, &data); err != nil {
		t.Fatalf("data is not proto3 JSON of store.v1.ReferencedResourceDeleted: %v\n%s", err, e.Data)
	}
	if data.GetTarget() != "cards/1" || data.GetTargetType() != cardType || data.GetPurged() ||
		len(data.GetReferrers()) != 1 || data.GetReferrers()[0].GetReferrer() != "loans/2" ||
		data.GetReferrers()[0].GetOnDelete() != storepbv1.ReferenceOptions_ON_DELETE_NOTIFY {
		t.Fatalf("data: %v", &data)
	}
	if _, err := json.Marshal(e); err != nil {
		t.Fatal(err)
	}

	// A checker that cannot answer fails a delete...
	down := repox.CheckerFunc(func(context.Context, string, string) ([]repox.Referrer, error) { return nil, errors.New("down") })
	refs = repox.NewReferences().Referrers(cardType, down)
	if _, err := refs.CheckDelete(ctx, cardType, "cards/1"); err == nil {
		t.Fatal("unavailable checker must fail a delete")
	}
	// ...and keeps a row from a purge, which never fails.
	var kept []string
	refs.OnKept(func(_ context.Context, name, why string) { kept = append(kept, name+":"+why) })
	if !refs.KeepOnPurge(ctx, cardType, "cards/1") || len(kept) != 1 {
		t.Fatalf("unavailable checker must keep the row: %v", kept)
	}
	// A purge keeps a row any referrer names, whatever its policy.
	refs = repox.NewReferences().Referrers(cardType, fixedReferrers(allow))
	if !refs.KeepOnPurge(ctx, cardType, "cards/1") {
		t.Fatal("a purge keeps a referenced row")
	}
	if repox.NewReferences().KeepOnPurge(ctx, cardType, "cards/1") {
		t.Fatal("no checker, nothing to keep")
	}
}

// listSource is a ReferrerSource over a fixed list of names, paged by offset.
func listSource(field string, policy repox.OnDelete, names ...string) repox.ReferrerSource {
	return repox.ReferrerSource{
		ResourceType: "lending.example.com/Loan", Field: field, TargetType: cardType, OnDelete: policy,
		List: func(_ context.Context, _ string, size int32, token string) ([]string, string, error) {
			off := 0
			if token != "" {
				fmt.Sscan(token, &off)
			}
			end := min(off+int(size), len(names))
			next := ""
			if end < len(names) {
				next = fmt.Sprint(end)
			}
			return names[off:end], next, nil
		},
	}
}

func TestListReferrerPage(t *testing.T) {
	ctx := context.Background()
	sources := []repox.ReferrerSource{
		listSource("guarantor", repox.OnDeleteNotify, "loans/g1", "loans/g2", "loans/g3"),
		listSource("borrower", repox.OnDeleteRestrict, "loans/b1", "loans/b2"),
	}
	var got []string
	token := ""
	pages := 0
	for {
		page, next, err := repox.ListReferrerPage(ctx, sources, cardType, "cards/1", 2, token)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, r := range page {
			got = append(got, r.Field+"="+r.Name)
		}
		if next == "" {
			break
		}
		token = next
	}
	want := "borrower=loans/b1 borrower=loans/b2 guarantor=loans/g1 guarantor=loans/g2 guarantor=loans/g3"
	if strings.Join(got, " ") != want || pages != 3 {
		t.Fatalf("pages=%d got %v\nwant %s", pages, got, want)
	}
	if page, _, _ := repox.ListReferrerPage(ctx, sources, "other.example.com/X", "cards/1", 10, ""); len(page) != 0 {
		t.Fatalf("another target type has no referrers here: %v", page)
	}
	if _, _, err := repox.ListReferrerPage(ctx, sources, cardType, "cards/1", 10, "!!"); !errors.Is(err, repox.ErrInvalidArgument) {
		t.Fatalf("bad token: %v", err)
	}
	all, err := repox.LocalReferrers(sources...).Referrers(ctx, cardType, "cards/1")
	if err != nil || len(all) != 5 {
		t.Fatalf("LocalReferrers: %d %v", len(all), err)
	}
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	refs := repox.NewReferences().Resolver(cardType, repox.GetResolver(cards{"cards/1": true}.get))
	stored := []repox.StoredReference{
		{Name: "loans/1", Field: "borrower", ResourceType: cardType, Target: "cards/1"},
		{Name: "loans/2", Field: "borrower", ResourceType: cardType, Target: "cards/gone"},
		{Name: "loans/3", Field: "x", ResourceType: "unwired.example.com/X", Target: "xs/1"},
		{Name: "loans/4", Field: "guarantor", ResourceType: cardType, Target: "cards/gone2"},
	}
	page := func(_ context.Context, size int32, token string) ([]repox.StoredReference, string, error) {
		off := 0
		if token != "" {
			fmt.Sscan(token, &off)
		}
		end := min(off+int(size), len(stored))
		next := ""
		if end < len(stored) {
			next = fmt.Sprint(end)
		}
		return stored[off:end], next, nil
	}
	var heard int
	rep, err := repox.Reconcile(ctx, refs, repox.ReconcileOptions{PageSize: 1, SampleSize: 1, OnDangling: func(context.Context, repox.StoredReference) { heard++ }}, page)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Checked != 3 || rep.Dangling != 2 || rep.Unchecked != 1 || len(rep.Sample) != 1 || rep.Sample[0].Name != "loans/2" || heard != 2 {
		t.Fatalf("report: %+v heard=%d", rep, heard)
	}
}

func TestForeignKeyError(t *testing.T) {
	cause := errors.New(`ERROR: insert or update on table "loans" violates foreign key constraint "fk_loans_book" (SQLSTATE 23503)`)
	err := repox.ForeignKeyError(cause)
	var re *repox.ReferenceError
	if !errors.Is(err, repox.ErrForeignKey) || !errors.Is(err, cause) || !errors.As(err, &re) || re.Constraint != "fk_loans_book" || re.Reason() != repox.ReasonForeignKey {
		t.Fatalf("got %v", err)
	}
	if !repox.IsForeignKeyViolation(cause) || repox.IsForeignKeyViolation(errors.New("x")) {
		t.Fatal("IsForeignKeyViolation")
	}
}

// cardServer is a fake card service with an AIP Get whose request holds the
// name in field 1, as AIP-131 has it; StringValue is wire-identical.
type cardServer struct{ known cards }

func (s cardServer) get(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	req := new(wrapperspb.StringValue)
	if err := dec(req); err != nil {
		return nil, err
	}
	if !s.known[req.GetValue()] {
		return nil, status.Errorf(codes.NotFound, "%s not found", req.GetValue())
	}
	return &emptypb.Empty{}, nil
}

func dialBuf(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	register(srv)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestGRPCBinding(t *testing.T) {
	ctx := context.Background()
	conn := dialBuf(t, func(s *grpc.Server) {
		s.RegisterService(&grpc.ServiceDesc{
			ServiceName: "vcard.v1.Cards",
			HandlerType: (*any)(nil),
			Methods:     []grpc.MethodDesc{{MethodName: "GetCard", Handler: cardServer{cards{"cards/1": true}}.get}},
		}, struct{}{})
		storepbv1.RegisterReferenceServiceServer(s, repox.NewReferenceServer(
			listSource("borrower", repox.OnDeleteRestrict, "loans/b1", "loans/b2", "loans/b3"),
		))
	})

	// The existence check is the target's AIP Get, by method name.
	get := repox.GRPCGetResolver(conn, "vcard.v1.Cards/GetCard")
	if err := get.Resolve(ctx, cardType, "cards/1"); err != nil {
		t.Fatalf("existing: %v", err)
	}
	if err := get.Resolve(ctx, cardType, "cards/2"); !errors.Is(err, repox.ErrReferenceNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if !repox.IsNotFound(status.Error(codes.NotFound, "x")) {
		t.Fatal("the gRPC binding teaches IsNotFound codes.NotFound")
	}

	// ListReferrers, through the client-side checker (paged by 1000).
	got, err := repox.ReferenceServiceChecker(storepbv1.NewReferenceServiceClient(conn)).Referrers(ctx, cardType, "cards/1")
	if err != nil || len(got) != 3 || got[0].OnDelete != repox.OnDeleteRestrict || got[2].Name != "loans/b3" {
		t.Fatalf("checker: %v %v", got, err)
	}
	// Paged by the caller, and the filter is the documented one.
	client := storepbv1.NewReferenceServiceClient(conn)
	resp, err := client.ListReferrers(ctx, &storepbv1.ListReferrersRequest{Parent: "cards/1", PageSize: 2, Filter: `resource_type = "vcard.example.com/Card"`})
	if err != nil || len(resp.GetReferrers()) != 2 || resp.GetNextPageToken() == "" {
		t.Fatalf("page 1: %v %v", resp, err)
	}
	resp, err = client.ListReferrers(ctx, &storepbv1.ListReferrersRequest{Parent: "cards/1", PageSize: 2, PageToken: resp.GetNextPageToken(), Filter: `resource_type = "vcard.example.com/Card"`})
	if err != nil || len(resp.GetReferrers()) != 1 || resp.GetNextPageToken() != "" {
		t.Fatalf("page 2: %v %v", resp, err)
	}
	if _, err := client.ListReferrers(ctx, &storepbv1.ListReferrersRequest{Parent: "cards/1", Filter: "field = 1"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unsupported filter: %v", err)
	}
	if _, err := client.ListReferrers(ctx, &storepbv1.ListReferrersRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing parent: %v", err)
	}

	// WireEndpoints dials each target once and registers both halves.
	t.Setenv("CARDS_GRPC", "bufnet")
	refs := repox.NewReferences()
	dials := 0
	err = repox.WireEndpoints(refs, []repox.ReferenceEndpoint{
		{Type: cardType, Get: "vcard.v1.Cards/GetCard", Target: "${CARDS_GRPC}", Referrers: []string{cardType}},
		{Type: "unset.example.com/X", Get: "x.v1.X/GetX", Target: "${UNSET_TARGET_FOR_TEST}"},
	}, func(string) (grpc.ClientConnInterface, error) { dials++; return conn, nil })
	if err != nil || dials != 1 {
		t.Fatalf("wire: %v dials=%d", err, dials)
	}
	if err := refs.Resolve(ctx, "borrower", cardType, "cards/2"); !errors.Is(err, repox.ErrReferenceNotFound) {
		t.Fatalf("wired resolver: %v", err)
	}
	if _, err := refs.CheckDelete(ctx, cardType, "cards/1"); !errors.Is(err, repox.ErrReferenced) {
		t.Fatalf("wired checker: %v", err)
	}
	if len(repox.ReferenceEndpoints) != 1 || repox.ReferenceEndpoints[0].Get != "vcard.v1.Cards/GetCard" {
		t.Fatalf("generated endpoints: %+v", repox.ReferenceEndpoints)
	}
}
