{{.Header}}

// Package repox is the shared runtime of the generated repositories: the error
// vocabulary adapters map their backend errors onto, the connection bundle the
// per-schema factories pick an adapter from, the list-request shape every
// repository List takes, and the small helpers (resource-name splitting, ULID
// minting, mask matching) the generated method bodies share.
package repox

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"{{.FilterxImport}}"
{{- if .GraphQLModule}}
	{{.GraphQLPkg}} "{{.GraphQLModule}}"
{{- end}}
)

// Error sentinels the repository interfaces speak. Callers translate them to
// their transport's codes (e.g. gRPC NotFound / AlreadyExists / Aborted /
// InvalidArgument) with errors.Is.
var (
	ErrNotFound        = errors.New("not found")
	ErrAlreadyExists   = errors.New("already exists")
	ErrConflict        = errors.New("conflict")
	ErrInvalidArgument = errors.New("invalid argument")
)

// Conn bundles the live backend handles a repository factory can adapt.
type Conn struct {
	Gorm *gorm.DB
{{- if .GraphQLModule}}
	GraphQL *{{.GraphQLPkg}}.Service
{{- end}}
	// Changes, when set, hears about every write a repository or Batch built
	// from this Conn commits. It is on the connection rather than on one
	// repository because a process builds many repositories over one
	// connection, and something that must see every write -- a cache dropping
	// what a write made stale -- cannot depend on each of them being wired.
	Changes ChangeObserver
}

// ChangeObserver hears that the record named name was written: created,
// updated, deleted, restored or purged. It runs after the write is durable, so
// it can neither veto nor alter it, and once per record a Batch commits.
//
// It is told only the name. That is enough to know which resource and which
// parent changed, and it keeps an observer from coming to depend on the
// record's contents, which a delete does not have.
type ChangeObserver interface {
	Changed(ctx context.Context, name string)
}

// Changes fans one notification out to several observers, in order. A nil
// entry is skipped.
type Changes []ChangeObserver

// Changed tells every observer in cs.
func (cs Changes) Changed(ctx context.Context, name string) {
	for _, c := range cs {
		if c != nil {
			c.Changed(ctx, name)
		}
	}
}

// ListInput is the request shape every repository List takes: AIP page
// controls, an order_by string, and a raw AIP-160 filter expression.
type ListInput struct {
	PageSize  int32
	PageToken string
	OrderBy   string
	Filter    string
	// ShowDeleted includes soft-deleted records (AIP-164 show_deleted); it
	// has no effect on resources without soft delete.
	ShowDeleted bool
	// Where are typed conditions ANDed with Filter, for a service building a
	// query in code rather than passing a caller's filter through: a batched
	// read (filterx.In), or a strict bound AIP-160 text would also express.
	// Field names are the filter fields Filter accepts.
	Where []filterx.Condition
}

// MapGormErr translates GORM's storage errors onto the repox sentinels,
// passing everything else (including sentinels already mapped inside a
// transaction) through unchanged.
func MapGormErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey), sqlState(err) == "23505":
		return ErrAlreadyExists
	case sqlState(err) == "23P01":
		// An exclusion constraint, (store.v1.table).no_overlap: the write
		// would overlap a row that already holds that time. Another writer
		// won the race; retrying against fresh state is meaningful.
		return ErrConflict
	case sqlState(err) == "40P01", sqlState(err) == "40001":
		// A deadlock or serialization failure: the database rolled the
		// transaction back to let a concurrent one through. Two writers
		// inserting conflicting rows under an exclusion constraint can each
		// wait on the other, so under contention this is the race being lost,
		// and like it, worth retrying.
		return ErrConflict
	default:
		return err
	}
}

// sqlState returns the SQLSTATE a driver error carries, or "". pgx's PgError
// exposes it as SQLState(); matching the method rather than the type keeps
// this package free of a driver import.
func sqlState(err error) string {
	var s interface{ SQLState() string }
	if errors.As(err, &s) {
		return s.SQLState()
	}
	return ""
}

// MapFilterxErr translates the filter/order engines' rejection onto
// ErrInvalidArgument, keeping the descriptive message.
func MapFilterxErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, filterx.ErrInvalid) {
		return fmt.Errorf("%w: %s", ErrInvalidArgument, err.Error())
	}
	return err
}

// SplitName splits an AIP resource name into its bare ids, validating the
// collection segments: SplitName("organisations/o1/members/m1",
// "organisations", "members") → ["o1", "m1"]. Any other shape is rejected
// with ErrInvalidArgument.
func SplitName(name string, collections ...string) ([]string, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 2*len(collections) {
		return nil, fmt.Errorf("%w: malformed resource name %q", ErrInvalidArgument, name)
	}
	ids := make([]string, 0, len(collections))
	for i, c := range collections {
		if parts[2*i] != c || parts[2*i+1] == "" {
			return nil, fmt.Errorf("%w: malformed resource name %q", ErrInvalidArgument, name)
		}
		ids = append(ids, parts[2*i+1])
	}
	return ids, nil
}

// LastSegment returns the final path component of a resource name — the bare
// id a reference column stores ("users/7" → "7").
func LastSegment(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// InMask reports whether the mask selects field: an empty mask selects
// everything (full replace), otherwise an exact path match.
func InMask(paths []string, field string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		if p == field {
			return true
		}
	}
	return false
}

// GroupTouched reports whether the mask touches field or any of its subpaths —
// message-typed fields are replaced wholesale when any part is selected.
func GroupTouched(paths []string, field string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		if p == field || strings.HasPrefix(p, field+".") {
			return true
		}
	}
	return false
}

// Ptr returns a pointer to v, for optional model columns.
func Ptr[T any](v T) *T { return &v }

// Deref returns *p, or T's zero value when p is nil.
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// ulidMu guards the monotonic state NewULID carries between calls.
var (
	ulidMu       sync.Mutex
	ulidLastMs   uint64
	ulidLastRand [10]byte
)

// crockford is the ULID alphabet (Crockford base32, no I/L/O/U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewULID mints a 26-character ULID: 48 bits of millisecond timestamp and 80
// bits of crypto randomness, Crockford-base32 encoded, with no external
// dependency.
//
// Monotonic within the process, per the spec's monotonicity section: a ULID
// minted in the same millisecond as the previous one increments its random
// part instead of drawing a fresh one. Keys therefore sort in the order they
// were minted, which is what lets a repeated field stored as child rows be
// read back in the order it was written.
func NewULID() string {
	ulidMu.Lock()
	ms := uint64(time.Now().UnixMilli())
	if ms <= ulidLastMs {
		ms = ulidLastMs
		// Increment the 80-bit random part; on the (astronomically unlikely)
		// overflow, move to the next millisecond.
		i := 9
		for ; i >= 0; i-- {
			ulidLastRand[i]++
			if ulidLastRand[i] != 0 {
				break
			}
		}
		if i < 0 {
			ms++
			if _, err := rand.Read(ulidLastRand[:]); err != nil {
				ulidMu.Unlock()
				panic(fmt.Sprintf("repox: reading crypto/rand: %v", err))
			}
		}
	} else if _, err := rand.Read(ulidLastRand[:]); err != nil {
		ulidMu.Unlock()
		panic(fmt.Sprintf("repox: reading crypto/rand: %v", err))
	}
	ulidLastMs = ms
	var b [16]byte
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	copy(b[6:], ulidLastRand[:])
	ulidMu.Unlock()

	// Encode the 128 bits as 26 base32 chars, most significant first: char c
	// covers bits [5c, 5c+5) of a virtual 130-bit value whose top two bits are
	// zero (so the first char is always 0-7, per the canonical spec).
	bitAt := func(i int) byte { // i in [0,130), 0 = most significant
		if i < 2 {
			return 0
		}
		i -= 2
		return (b[i/8] >> (7 - i%8)) & 1
	}
	var out [26]byte
	for c := 0; c < 26; c++ {
		var v byte
		for j := 0; j < 5; j++ {
			v = v<<1 | bitAt(c*5+j)
		}
		out[c] = crockford[v]
	}
	return string(out[:])
}
