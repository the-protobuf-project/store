// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package search_test

// pit_test.go runs the generated OpenSearch search against a fake cluster
// (httptest) that counts points in time: a first page opens none, so no
// number of searches can exhaust search.max_open_pit_context; a paged query
// holds at most one, deleted with its last page; Iterate deletes its own
// however the loop ends; and a cluster out of them fails with
// osx.ErrPointInTimeLimit. No real cluster is needed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oh-tarnished/opensearch-go-sdk/opensearch"

	"github.com/the-protobuf-project/store/examples/generated/go/opensearch/search/osx"
	"github.com/the-protobuf-project/store/examples/generated/go/opensearch/search/search/search"
	"github.com/the-protobuf-project/store/examples/generated/go/protobuf/search/searchdemov1"
	"github.com/the-protobuf-project/store/examples/generated/go/repository/search/repox"
)

// pitLimitBody is what OpenSearch 3.x answers (429) when the limit is hit.
const pitLimitBody = `{"error":{"root_cause":[{"type":"rejected_execution_exception","reason":"Trying to create too many Point In Time contexts. Must be less than or equal to: [300]. This limit can be set by changing the [search.max_open_pit_context] setting."}],"type":"search_phase_execution_exception","reason":"all shards failed"},"status":429}`

// fakeCluster serves one alias of documents, in order, each hit's sort value
// its position; search_after resumes after it. It counts points in time.
type fakeCluster struct {
	alias string
	docs  []json.RawMessage

	mu       sync.Mutex
	open     map[string]bool
	opened   int
	maxOpen  int
	limit    int // refuse a point in time past this many open; 0 = no limit
	failFrom int // fail every search from this one on (1-based); 0 = never
	searches int
}

func newFakeCluster(t *testing.T, ix *osx.Index[*searchdemov1.RoomDay], n int) (*fakeCluster, *opensearch.Client) {
	t.Helper()
	f := &fakeCluster{alias: ix.Spec.Alias, open: map[string]bool{}}
	for i := range n {
		r := &searchdemov1.Room{Name: fmt.Sprintf("rooms/r%03d", i), DisplayName: fmt.Sprintf("Room %03d", i), Floor: 1}
		_, doc, err := ix.Document(dayOf(r, days[0], nil))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(doc)
		f.docs = append(f.docs, raw)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := opensearch.New(opensearch.Config{URL: srv.URL, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func (f *fakeCluster) openNow() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.open)
}

func (f *fakeCluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/"+f.alias+"/_search/point_in_time":
		if f.limit > 0 && len(f.open) >= f.limit {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, pitLimitBody)
			return
		}
		f.opened++
		id := "pit-" + strconv.Itoa(f.opened)
		f.open[id] = true
		f.maxOpen = max(f.maxOpen, len(f.open))
		_, _ = fmt.Fprintf(w, `{"pit_id":%q}`, id)
	case r.Method == http.MethodDelete && r.URL.Path == "/_search/point_in_time":
		var req struct {
			PitID []string `json:"pit_id"`
		}
		_ = json.Unmarshal(body, &req)
		for _, id := range req.PitID {
			delete(f.open, id)
		}
		_, _ = io.WriteString(w, `{"pits":[]}`)
	case r.Method == http.MethodPost && (r.URL.Path == "/"+f.alias+"/_search" || r.URL.Path == "/_search"):
		var req struct {
			Size int `json:"size"`
			PIT  *struct {
				ID string `json:"id"`
			} `json:"pit"`
			SearchAfter []int `json:"search_after"`
		}
		_ = json.Unmarshal(body, &req)
		f.searches++
		if f.failFrom > 0 && f.searches >= f.failFrom {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"type":"exception","reason":"boom"},"status":500}`)
			return
		}
		if (r.URL.Path == "/_search") != (req.PIT != nil) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"illegal_argument_exception","reason":"pit and index"},"status":400}`)
			return
		}
		if req.PIT != nil && !f.open[req.PIT.ID] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"type":"search_context_missing_exception","reason":"No search context found"},"status":404}`)
			return
		}
		from := 0
		if len(req.SearchAfter) == 1 {
			from = req.SearchAfter[0] + 1
		}
		var hits []string
		for i := from; i < len(f.docs) && len(hits) < req.Size; i++ {
			hits = append(hits, fmt.Sprintf(`{"_id":"%d","_source":%s,"sort":[%d]}`, i, f.docs[i], i))
		}
		pit := ""
		if req.PIT != nil {
			pit = fmt.Sprintf(`"pit_id":%q,`, req.PIT.ID)
		}
		_, _ = fmt.Fprintf(w, `{%s"hits":{"hits":[%s]}}`, pit, strings.Join(hits, ","))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"type":"not_found","reason":"%s %s"},"status":404}`, r.Method, r.URL.Path)
	}
}

func pitIndex() *osx.Index[*searchdemov1.RoomDay] {
	ix := *search.RoomDayIndex
	ix.Spec.Alias = "pit_room_days"
	return &ix
}

func TestFirstPagesOpenNoPointInTime(t *testing.T) {
	ctx := context.Background()
	for _, keepAlive := range []time.Duration{0, -1, 30 * time.Second} {
		ix := pitIndex()
		ix.PageKeepAlive = keepAlive
		f, c := newFakeCluster(t, ix, 25)
		f.limit = 300
		for i := range 1000 {
			rows, next, err := ix.Search(ctx, c, repox.SearchInput{PageSize: 5})
			if err != nil {
				t.Fatalf("keep-alive %v: search %d: %v", keepAlive, i, err)
			}
			if len(rows) != 5 || next == "" {
				t.Fatalf("keep-alive %v: first page = %d rows, next %q", keepAlive, len(rows), next)
			}
		}
		if f.opened != 0 {
			t.Errorf("keep-alive %v: 1,000 first pages opened %d points in time, want 0", keepAlive, f.opened)
		}
	}
}

func TestPagesWithoutPointInTime(t *testing.T) {
	ix := pitIndex()
	f, c := newFakeCluster(t, ix, 23)
	all, pages := pageAll(t, repox.SearchInput{PageSize: 5}, func(in repox.SearchInput) ([]*searchdemov1.RoomDay, string, error) {
		return ix.Search(context.Background(), c, in)
	})
	checkAllRooms(t, all, 23)
	if pages != 5 {
		t.Errorf("pages = %d, want 5", pages)
	}
	if f.opened != 0 {
		t.Errorf("paging opened %d points in time, want 0", f.opened)
	}
}

func TestPagedQueryHoldsOnePointInTime(t *testing.T) {
	ctx := context.Background()
	ix := pitIndex()
	ix.PageKeepAlive = 30 * time.Second
	f, c := newFakeCluster(t, ix, 23)

	// Paged to the end: one point in time, opened for page 2, deleted with
	// the last page.
	all, _ := pageAll(t, repox.SearchInput{PageSize: 5}, func(in repox.SearchInput) ([]*searchdemov1.RoomDay, string, error) {
		rows, next, err := ix.Search(ctx, c, in)
		if in.PageToken != "" && next != "" && f.openNow() != 1 {
			t.Errorf("mid-query: %d points in time open, want 1", f.openNow())
		}
		return rows, next, err
	})
	checkAllRooms(t, all, 23)
	if f.opened != 1 || f.openNow() != 0 {
		t.Errorf("a paged query opened %d points in time and left %d open, want 1 and 0", f.opened, f.openNow())
	}

	// Many queries abandoned after page 2 each hold one until it expires;
	// none abandoned after page 1 holds any.
	for range 10 {
		_, next, err := ix.Search(ctx, c, repox.SearchInput{PageSize: 5})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ix.Search(ctx, c, repox.SearchInput{PageSize: 5, PageToken: next}); err != nil {
			t.Fatal(err)
		}
	}
	if f.openNow() != 10 {
		t.Errorf("10 queries abandoned after page 2 hold %d points in time, want 10", f.openNow())
	}

	// A token whose point in time expired says so.
	_, next, _ := ix.Search(ctx, c, repox.SearchInput{PageSize: 5})
	_, next, _ = ix.Search(ctx, c, repox.SearchInput{PageSize: 5, PageToken: next})
	f.mu.Lock()
	clear(f.open)
	f.mu.Unlock()
	if _, _, err := ix.Search(ctx, c, repox.SearchInput{PageSize: 5, PageToken: next}); !errors.Is(err, repox.ErrInvalidArgument) {
		t.Errorf("expired point in time: err = %v, want ErrInvalidArgument", err)
	}

	// A token minted on the live index still pages on it when the index
	// stops using points in time.
	live := *ix
	live.PageKeepAlive = 0
	if _, _, err := live.Search(ctx, c, repox.SearchInput{PageSize: 5, PageToken: next}); err != nil {
		t.Errorf("a point-in-time token on the live index: %v", err)
	}
}

func TestPointInTimeLimitIsTyped(t *testing.T) {
	ctx := context.Background()
	ix := pitIndex()
	ix.PageKeepAlive = 30 * time.Second
	f, c := newFakeCluster(t, ix, 23)
	f.limit = 1
	f.open["someone-else"] = true

	// A first page needs none, so it still works.
	_, next, err := ix.Search(ctx, c, repox.SearchInput{PageSize: 5})
	if err != nil {
		t.Fatalf("first page at the limit: %v", err)
	}
	_, _, err = ix.Search(ctx, c, repox.SearchInput{PageSize: 5, PageToken: next})
	if !errors.Is(err, osx.ErrPointInTimeLimit) || errors.Is(err, repox.ErrInvalidArgument) {
		t.Errorf("page 2 at the limit: err = %v, want ErrPointInTimeLimit (not ErrInvalidArgument)", err)
	}
	var oe *opensearch.Error
	if !errors.As(err, &oe) || oe.StatusCode != http.StatusTooManyRequests {
		t.Errorf("page 2 at the limit does not wrap OpenSearch's 429: %v", err)
	}
	for _, err := range ix.Iterate(ctx, c, repox.SearchInput{}) {
		if !errors.Is(err, osx.ErrPointInTimeLimit) {
			t.Errorf("Iterate at the limit: err = %v, want ErrPointInTimeLimit", err)
		}
		break
	}
	// Live paging never asks for one.
	live := *ix
	live.PageKeepAlive = 0
	all, _ := pageAll(t, repox.SearchInput{PageSize: 5}, func(in repox.SearchInput) ([]*searchdemov1.RoomDay, string, error) {
		return live.Search(ctx, c, in)
	})
	checkAllRooms(t, all, 23)
}

func TestIterateDeletesItsPointInTime(t *testing.T) {
	ctx := context.Background()
	ix := pitIndex()
	f, c := newFakeCluster(t, ix, 2500)

	n := 0
	for _, err := range ix.Iterate(ctx, c, repox.SearchInput{}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 2500 || f.openNow() != 0 {
		t.Errorf("Iterate to the end: %d documents, %d points in time left open", n, f.openNow())
	}

	// Stopped early by the caller.
	for range ix.Iterate(ctx, c, repox.SearchInput{}) {
		break
	}
	if f.openNow() != 0 {
		t.Errorf("Iterate stopped early left %d points in time open", f.openNow())
	}

	// Failed on its second page.
	f.failFrom = f.searches + 2
	var failed error
	for _, err := range ix.Iterate(ctx, c, repox.SearchInput{}) {
		if err != nil {
			failed = err
		}
	}
	if failed == nil || errors.Is(failed, osx.ErrPointInTimeLimit) {
		t.Errorf("Iterate on a failing cluster: err = %v", failed)
	}
	if f.openNow() != 0 || f.maxOpen != 1 {
		t.Errorf("Iterate that failed left %d points in time open (at most %d at once)", f.openNow(), f.maxOpen)
	}
}

// checkAllRooms asserts rows are rooms r000..r<n-1>, each once, in order.
func checkAllRooms(t *testing.T, rows []*searchdemov1.RoomDay, n int) {
	t.Helper()
	if len(rows) != n {
		t.Fatalf("paged %d rows, want %d", len(rows), n)
	}
	for i, r := range rows {
		if want := fmt.Sprintf("rooms/r%03d", i); r.GetRoom() != want {
			t.Fatalf("row %d is %s, want %s", i, r.GetRoom(), want)
		}
	}
}
