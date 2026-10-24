// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package ddnrefs is a Hasura DDN engine plugin that holds writes sent
// straight to DDN's GraphQL API -- past the generated repositories -- to the
// same store.v1 reference contract the repositories enforce:
//
//   - pre-parse (LifecyclePluginHook, pre: parse): an insert or update that
//     sets a reference field no foreign key covers is checked with the
//     target's AIP Get by name; a delete is checked against the referrers the
//     referring services' store.v1.ReferenceService reports, and an
//     ON_DELETE_RESTRICT referrer refuses it. A refusal is a 400 the engine
//     returns to the client as a GraphQL error.
//   - pre-response (pre: response): once a delete has removed rows, the
//     ON_DELETE_NOTIFY referrers get their CloudEvent
//     (org.the-protobuf-project.store.v1.reference.deleted).
//
// It reads only the configuration protoc-gen-store emits (ddn/references.json)
// and the deployment facts there; nothing in it is specific to a project.
//
// Engine plugin protocol, from the Hasura DDN docs (plugins/introduction,
// reference/metadata-reference/engine-plugins): the engine POSTs
// {"rawRequest": {"query", "variables", "operationName"}, "session": {...}}
// to a pre-parse plugin, which answers 204 to continue, 400 with an error
// object for a user error, 500 for an internal one (200 and 299 replace the
// response or the query; not used here). A pre-response plugin receives
// {"response", "rawRequest", "session"} and, in asynchronous mode, its answer
// is ignored.
package ddnrefs

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// EventType is the CloudEvents type of a NOTIFY event (see
// store.v1.ReferencedResourceDeleted).
const EventType = "org.the-protobuf-project.store.v1.reference.deleted"

// Options are the deployment side of the plugin.
type Options struct {
	// Dial connects to an endpoint's gRPC target (already expanded from the
	// environment).
	Dial func(target string) (grpc.ClientConnInterface, error)
	// DDNURL is the engine's GraphQL endpoint, used to read the name of a
	// nested resource a delete names by key. DDNHeaders go with it (an admin
	// secret).
	DDNURL     string
	DDNHeaders map[string]string
	// Secret, when set, is the value the engine must send in the
	// hasura-m-auth header (the hook's config.request.headers.additional).
	Secret string
	// Sink receives NOTIFY CloudEvents (HTTP POST, structured JSON mode);
	// Source is their source attribute. Empty Sink logs and drops them.
	Sink   string
	Source string
	// HTTPClient makes the DDN and sink calls; http.DefaultClient otherwise.
	HTTPClient *http.Client
	// Logger; slog.Default otherwise.
	Logger *slog.Logger
	// Getenv expands ${NAME} targets; os.Getenv when nil is not wanted is
	// the caller's choice (cmd passes os.Getenv).
	Getenv func(string) string
}

// Plugin serves the two hooks.
type Plugin struct {
	mutations map[string]*Mutation
	getters   map[string]getter                             // by resource type
	referrers map[string][]storepbv1.ReferenceServiceClient // by our resource type
	o         Options
	http      *http.Client
	log       *slog.Logger

	mu      sync.Mutex
	pending map[string]pendingDelete // field + key -> what a delete will notify
}

type getter struct {
	conn   grpc.ClientConnInterface
	method string
}

// pendingDelete is a delete pre-parse let through with NOTIFY referrers, kept
// until pre-response sees it done.
type pendingDelete struct {
	resourceType, name string
	notify             []*storepbv1.Referrer
	until              time.Time
}

// New builds the plugin over cfg, dialing every endpoint once.
func New(cfg *Config, o Options) (*Plugin, error) {
	p := &Plugin{
		mutations: map[string]*Mutation{},
		getters:   map[string]getter{},
		referrers: map[string][]storepbv1.ReferenceServiceClient{},
		o:         o,
		http:      o.HTTPClient,
		log:       o.Logger,
		pending:   map[string]pendingDelete{},
	}
	if p.http == nil {
		p.http = http.DefaultClient
	}
	if p.log == nil {
		p.log = slog.Default()
	}
	for i := range cfg.Mutations {
		p.mutations[cfg.Mutations[i].Field] = &cfg.Mutations[i]
	}
	conns := map[string]grpc.ClientConnInterface{}
	for _, e := range cfg.Endpoints {
		target := e.Target
		if strings.HasPrefix(target, "${") && strings.HasSuffix(target, "}") && o.Getenv != nil {
			target = o.Getenv(target[2 : len(target)-1])
		}
		if target == "" {
			p.log.Warn("ddnrefs: endpoint without a target; its checks are off", "type", e.Type)
			continue
		}
		conn, ok := conns[target]
		if !ok {
			c, err := o.Dial(target)
			if err != nil {
				return nil, fmt.Errorf("dial %s: %w", target, err)
			}
			conns[target], conn = c, c
		}
		if e.Type != "" && e.Get != "" {
			p.getters[e.Type] = getter{conn: conn, method: "/" + strings.TrimPrefix(e.Get, "/")}
		}
		for _, t := range e.Referrers {
			p.referrers[t] = append(p.referrers[t], storepbv1.NewReferenceServiceClient(conn))
		}
	}
	return p, nil
}

// hookRequest is what the engine POSTs to both hooks.
type hookRequest struct {
	RawRequest struct {
		Query         string         `json:"query"`
		Variables     map[string]any `json:"variables"`
		OperationName string         `json:"operationName"`
	} `json:"rawRequest"`
	Response *struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []json.RawMessage          `json:"errors"`
	} `json:"response"`
}

// refusal is a user error the engine returns to the client.
type refusal struct {
	Message    string            `json:"message"`
	Extensions map[string]string `json:"extensions"`
}

func (r *refusal) Error() string { return r.Message }

func refuse(reason, format string, args ...any) *refusal {
	return &refusal{
		Message: fmt.Sprintf(format, args...),
		Extensions: map[string]string{
			"code":   "FAILED_PRECONDITION",
			"reason": reason,
			"domain": "store.the-protobuf-project.org",
		},
	}
}

// PreParse is the pre: parse hook.
func (p *Plugin) PreParse() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, ok := p.read(w, r)
		if !ok {
			return
		}
		err := p.check(r.Context(), req)
		var ref *refusal
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.As(err, &ref):
			writeJSON(w, http.StatusBadRequest, ref)
		default:
			p.log.Error("ddnrefs: check failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "reference check unavailable: " + err.Error()})
		}
	})
}

// PreResponse is the pre: response hook (asynchronous mode): it publishes the
// NOTIFY events of the deletes that removed rows.
func (p *Plugin) PreResponse() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, ok := p.read(w, r)
		if !ok {
			return
		}
		w.WriteHeader(http.StatusNoContent)
		if req.Response == nil || len(req.Response.Errors) > 0 {
			return
		}
		doc, op := operation(req)
		if op == nil {
			return
		}
		for _, f := range rootFields(doc, op.SelectionSet) {
			m := p.mutations[f.Name]
			if m == nil || m.Kind != "delete" {
				continue
			}
			key := keyOf(f, m, req.RawRequest.Variables)
			pd, ok := p.take(m.Field + "\x00" + key)
			if !ok || !removed(req.Response.Data[responseKey(f)]) {
				continue
			}
			p.notify(r.Context(), pd)
		}
	})
}

func (p *Plugin) read(w http.ResponseWriter, r *http.Request) (*hookRequest, bool) {
	if p.o.Secret != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("hasura-m-auth")), []byte(p.o.Secret)) != 1 {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "plugin: bad hasura-m-auth"})
		return nil, false
	}
	var req hookRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "plugin: " + err.Error()})
		return nil, false
	}
	return &req, true
}

// operation parses the request and picks the operation it runs; nil when it
// does not parse (the engine reports that itself) or names none.
func operation(req *hookRequest) (*ast.QueryDocument, *ast.OperationDefinition) {
	doc, err := parser.ParseQuery(&ast.Source{Input: req.RawRequest.Query})
	if err != nil {
		return nil, nil
	}
	if name := req.RawRequest.OperationName; name != "" {
		return doc, doc.Operations.ForName(name)
	}
	if len(doc.Operations) == 1 {
		return doc, doc.Operations[0]
	}
	return doc, nil
}

// rootFields flattens an operation's root selection set, fragments included.
func rootFields(doc *ast.QueryDocument, set ast.SelectionSet) []*ast.Field {
	var out []*ast.Field
	for _, s := range set {
		switch s := s.(type) {
		case *ast.Field:
			out = append(out, s)
		case *ast.InlineFragment:
			out = append(out, rootFields(doc, s.SelectionSet)...)
		case *ast.FragmentSpread:
			if fr := doc.Fragments.ForName(s.Name); fr != nil {
				out = append(out, rootFields(doc, fr.SelectionSet)...)
			}
		}
	}
	return out
}

// check runs pre-parse's checks over every mutation root field.
func (p *Plugin) check(ctx context.Context, req *hookRequest) error {
	doc, op := operation(req)
	if op == nil || op.Operation != ast.Mutation {
		return nil
	}
	vars := req.RawRequest.Variables
	for _, f := range rootFields(doc, op.SelectionSet) {
		m := p.mutations[f.Name]
		if m == nil {
			continue
		}
		switch m.Kind {
		case "insert":
			for _, obj := range objects(argValue(f, vars, "objects", "object")) {
				if err := p.resolveAll(ctx, m, obj, false); err != nil {
					return err
				}
			}
		case "update":
			for _, obj := range objects(argValue(f, vars, "updateColumns", "_set", "set")) {
				if err := p.resolveAll(ctx, m, obj, true); err != nil {
					return err
				}
			}
		case "delete":
			if err := p.checkDelete(ctx, f, m, vars); err != nil {
				return err
			}
		}
	}
	return nil
}

// argValue is the first of names f has as an argument, variables resolved.
func argValue(f *ast.Field, vars map[string]any, names ...string) any {
	for _, n := range names {
		if a := f.Arguments.ForName(n); a != nil {
			v, err := a.Value.Value(vars)
			if err == nil {
				return v
			}
		}
	}
	return nil
}

// objects is v as a list of input objects.
func objects(v any) []map[string]any {
	switch v := v.(type) {
	case map[string]any:
		return []map[string]any{v}
	case []any:
		var out []map[string]any
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// resolveAll proves every reference obj sets names an existing resource. An
// update sets a column as {"set": v}.
func (p *Plugin) resolveAll(ctx context.Context, m *Mutation, obj map[string]any, update bool) error {
	for _, ref := range m.References {
		v, ok := obj[ref.Column]
		if !ok {
			continue
		}
		if update {
			if set, isObj := v.(map[string]any); isObj {
				if v, ok = set["set"]; !ok {
					v = set["_set"]
				}
			}
		}
		for _, name := range names(v) {
			if err := p.resolve(ctx, m, ref, name); err != nil {
				return err
			}
		}
	}
	return nil
}

func names(v any) []string {
	switch v := v.(type) {
	case string:
		if v != "" {
			return []string{v}
		}
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// resolve calls the target's AIP Get by name: the request carries the name
// in field 1 (AIP-131), which a StringValue matches on the wire.
func (p *Plugin) resolve(ctx context.Context, m *Mutation, ref Reference, name string) error {
	g, ok := p.getters[ref.Type]
	if !ok {
		p.log.Warn("ddnrefs: no Get endpoint for a reference type; not checked", "type", ref.Type, "field", ref.Field)
		return nil
	}
	err := g.conn.Invoke(ctx, g.method, wrapperspb.String(name), new(emptypb.Empty))
	switch {
	case err == nil:
		return nil
	case status.Code(err) == codes.NotFound:
		return refuse("REFERENCE_NOT_FOUND", "%s: %s names %s %q, which does not exist", m.Field, ref.Field, ref.Type, name)
	}
	return fmt.Errorf("get %s %q: %w", ref.Type, name, err)
}

// keyOf is a delete's key argument as text.
func keyOf(f *ast.Field, m *Mutation, vars map[string]any) string {
	arg := m.KeyArgument
	if arg == "" {
		arg = "keyId"
	}
	switch v := argValue(f, vars, arg).(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// checkDelete asks the referring services about the row a delete removes.
func (p *Plugin) checkDelete(ctx context.Context, f *ast.Field, m *Mutation, vars map[string]any) error {
	clients := p.referrers[m.ResourceType]
	if len(clients) == 0 {
		return nil
	}
	key := keyOf(f, m, vars)
	if key == "" {
		return nil
	}
	name, err := p.nameOf(ctx, m, key)
	if err != nil || name == "" {
		return err // no such row: nothing to delete, nothing to check
	}
	var restrict, notify []*storepbv1.Referrer
	for _, c := range clients {
		req := &storepbv1.ListReferrersRequest{Parent: name, PageSize: 1000, Filter: fmt.Sprintf("resource_type = %q", m.ResourceType)}
		for {
			resp, err := c.ListReferrers(ctx, req)
			if err != nil {
				return fmt.Errorf("referrers of %q: %w", name, err)
			}
			for _, r := range resp.GetReferrers() {
				switch r.GetOnDelete() {
				case storepbv1.ReferenceOptions_ON_DELETE_RESTRICT:
					restrict = append(restrict, r)
				case storepbv1.ReferenceOptions_ON_DELETE_NOTIFY:
					notify = append(notify, r)
				}
			}
			if resp.GetNextPageToken() == "" {
				break
			}
			req.PageToken = resp.GetNextPageToken()
		}
	}
	if len(restrict) > 0 {
		var by []string
		for _, r := range restrict {
			by = append(by, r.GetReferrer()+" ("+r.GetField()+")")
		}
		return refuse("RESOURCE_REFERENCED", "%s: %s %q is still named by %s", m.Field, m.ResourceType, name, strings.Join(by, ", "))
	}
	if len(notify) > 0 {
		p.put(m.Field+"\x00"+key, pendingDelete{resourceType: m.ResourceType, name: name, notify: notify, until: time.Now().Add(5 * time.Minute)})
	}
	return nil
}

// nameOf is the resource name of the row a delete names by key: built from
// the pattern for a top-level resource, read from DDN for a nested one.
func (p *Plugin) nameOf(ctx context.Context, m *Mutation, key string) (string, error) {
	segs := strings.Split(m.Pattern, "/")
	if len(segs) == 2 {
		return segs[0] + "/" + key, nil
	}
	if p.o.DDNURL == "" || m.ByIDQuery == "" {
		return "", fmt.Errorf("%s: a nested resource's name needs DDN_URL to read it", m.Field)
	}
	k, _ := json.Marshal(key)
	body, _ := json.Marshal(map[string]string{"query": fmt.Sprintf("query { r: %s(id: %s) { name } }", m.ByIDQuery, k)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.o.DDNURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for h, v := range p.o.DDNHeaders {
		req.Header.Set(h, v)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			R *struct {
				Name string `json:"name"`
			} `json:"r"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Errors) > 0 {
		return "", fmt.Errorf("reading %s: %s", m.ByIDQuery, out.Errors[0].Message)
	}
	if out.Data.R == nil {
		return "", nil
	}
	return out.Data.R.Name, nil
}

func (p *Plugin) put(k string, d pendingDelete) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, v := range p.pending {
		if now.After(v.until) {
			delete(p.pending, k)
		}
	}
	p.pending[k] = d
}

func (p *Plugin) take(k string) (pendingDelete, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, ok := p.pending[k]
	delete(p.pending, k)
	return d, ok
}

// responseKey is where f's result sits in the response.
func responseKey(f *ast.Field) string {
	if f.Alias != "" {
		return f.Alias
	}
	return f.Name
}

// removed reports whether a delete's result says it removed a row.
func removed(raw json.RawMessage) bool {
	var r map[string]any
	if json.Unmarshal(raw, &r) != nil {
		return false
	}
	for _, k := range []string{"affectedRows", "affected_rows"} {
		if n, ok := r[k].(float64); ok {
			return n > 0
		}
	}
	return false
}

// notify publishes d's CloudEvent to the sink.
func (p *Plugin) notify(ctx context.Context, d pendingDelete) {
	type jsonReferrer struct {
		Referrer     string `json:"referrer,omitempty"`
		ResourceType string `json:"resourceType,omitempty"`
		Field        string `json:"field,omitempty"`
		OnDelete     string `json:"onDelete,omitempty"`
	}
	data := struct {
		Target     string         `json:"target"`
		TargetType string         `json:"targetType"`
		Purged     bool           `json:"purged"`
		Referrers  []jsonReferrer `json:"referrers"`
	}{Target: d.name, TargetType: d.resourceType, Purged: true}
	for _, r := range d.notify {
		data.Referrers = append(data.Referrers, jsonReferrer{r.GetReferrer(), r.GetResourceType(), r.GetField(), r.GetOnDelete().String()})
	}
	raw, _ := json.Marshal(data)
	event, _ := json.Marshal(map[string]any{
		"specversion":     "1.0",
		"id":              fmt.Sprintf("%d", time.Now().UnixNano()),
		"source":          p.o.Source,
		"type":            EventType,
		"subject":         d.name,
		"time":            time.Now().UTC().Format(time.RFC3339Nano),
		"datacontenttype": "application/json",
		"dataschema":      "type.googleapis.com/store.v1.ReferencedResourceDeleted",
		"data":            json.RawMessage(raw),
	})
	if p.o.Sink == "" {
		p.log.Warn("ddnrefs: NOTIFY referrers but no sink; event dropped", "subject", d.name)
		return
	}
	req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodPost, p.o.Sink, bytes.NewReader(event))
	if err != nil {
		p.log.Error("ddnrefs: event", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	resp, err := p.http.Do(req)
	if err != nil {
		p.log.Error("ddnrefs: publishing an event", "error", err, "subject", d.name)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		p.log.Error("ddnrefs: the sink refused an event", "status", resp.StatusCode, "subject", d.name)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
