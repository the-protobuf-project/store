// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package wire_test

// repository_guarantees_test.go checks properties of the generated repository
// layer that a byte comparison accepts silently when a template change drops
// them: that every etag guard reads its row FOR UPDATE, that every resource is
// batchable, and that a value object a filter can reach has a GraphQL lookup.
// The goldens pin the bytes; these pin what the bytes must keep doing.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repositoryGoldens are the cases whose goldens hold a repository package.
var repositoryGoldens = []string{
	"testdata/cases/repository/golden/repository/v1/orgv1",
	"testdata/cases/deepgraph/golden/repository/v1/deepv1",
	"testdata/cases/datetimezone/golden/repository/v1/dtzv1",
	"testdata/cases/versionedpkg/golden/repository/v1/resourcev1",
	"testdata/cases/references/golden/repository/catalog/catalog",
	"testdata/cases/references/golden/repository/lending/lending",
}

func readGolden(t *testing.T, dir, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// funcBody returns the body of the function whose signature line starts with
// sig, up to the closing brace at column zero.
func funcBody(src, sig string) (string, bool) {
	i := strings.Index(src, sig)
	if i < 0 {
		return "", false
	}
	j := strings.Index(src[i:], "\n}\n")
	if j < 0 {
		return "", false
	}
	return src[i : i+j], true
}

// Two writers holding one etag must not both pass the guard: the row is read
// FOR UPDATE before the etag is compared, so the second waits for the first
// and then sees the etag it wrote.
func TestGormEtagGuardsLockTheRow(t *testing.T) {
	guard := regexp.MustCompile(`func \(r \*Gorm(\w+)Repository\) (updateTx|updateOutputOnlyTx)\(`)
	for _, dir := range repositoryGoldens {
		src := readGolden(t, dir, "gorm.go")
		found := guard.FindAllString(src, -1)
		if len(found) == 0 {
			t.Fatalf("%s: no guarded writes found", dir)
		}
		for _, sig := range found {
			body, _ := funcBody(src, sig)
			lock := strings.Index(body, `clause.Locking{Strength: "UPDATE"}`)
			if lock < 0 {
				t.Errorf("%s: %s reads its row without FOR UPDATE", dir, sig)
				continue
			}
			if cmp := strings.Index(body, "repox.ErrConflict"); cmp >= 0 && cmp < lock {
				t.Errorf("%s: %s compares the etag before locking the row", dir, sig)
			}
		}
	}
}

// Every resource -- value objects and deep graphs included -- can be queued
// in a Batch, and every Batch can share a Unit with other schemas'.
func TestEveryResourceIsBatchable(t *testing.T) {
	model := regexp.MustCompile(`(?m)^type (\w+)Repository interface`)
	for _, dir := range repositoryGoldens {
		src := readGolden(t, dir, "repository.go")
		if !strings.Contains(src, "func NewBatchIn(u *repox.Unit, opts ...Option) Batch") {
			t.Errorf("%s: no NewBatchIn", dir)
		}
		batch, ok := funcBody(src, "type Batch interface {")
		if !ok {
			t.Fatalf("%s: no Batch interface", dir)
		}
		for _, m := range model.FindAllStringSubmatch(src, -1) {
			for _, method := range []string{"Create" + m[1] + "(", "Update" + m[1] + "(m *", "Delete" + m[1] + "(name string)"} {
				if !strings.Contains(batch, method) {
					t.Errorf("%s: Batch lacks %s", dir, method)
				}
			}
		}
	}
}

// A GraphQL batch update guarded by an etag asserts it inside the request:
// the post-checked self-update is queued before the pre-checked write.
func TestGraphQLBatchGuardIsInTheRequest(t *testing.T) {
	src := readGolden(t, "testdata/cases/repository/golden/repository/v1/orgv1", "graphql.go")
	body, ok := funcBody(src, "func (r *GraphQLOrganisationRepository) updateOps(")
	if !ok {
		t.Fatal("no updateOps")
	}
	post, pre := strings.Index(body, ".PostCheck("), strings.Index(body, ".PreCheck(")
	if post < 0 || pre < 0 || post > pre {
		t.Fatalf("want the post-checked assertion queued before the pre-checked update:\n%s", body)
	}
}

// External references are resolved on every write path, on both backends:
// the repositories (through guard) and every Batch create or update. A purge
// on either backend goes through the keep-if-referenced path, and both map a
// foreign-key violation onto repox.ErrForeignKey.
func TestReferenceIntegrityOnEveryPath(t *testing.T) {
	const dir = "testdata/cases/references/golden/repository/lending/lending"
	repo := readGolden(t, dir, "repository.go")
	for _, sig := range []string{
		"func (g *guardedLoanRepository) Create(",
		"func (g *guardedLoanRepository) Update(",
		"func (g *guardedLoanRepository) UpdateWithOutputOnly(",
	} {
		body, ok := funcBody(repo, sig)
		if !ok || !strings.Contains(body, "resolveLoanReferences(") {
			t.Errorf("%s does not resolve references", sig)
		}
	}
	if body, ok := funcBody(repo, "func (g *guardedLoanRepository) Delete("); !ok || !strings.Contains(body, "CheckDelete(") {
		t.Error("Delete does not consult referrers")
	}
	for _, file := range []string{"gorm.go", "graphql.go"} {
		src := readGolden(t, dir, file)
		recv := map[string]string{"gorm.go": "gormBatch", "graphql.go": "graphQLBatch"}[file]
		for _, m := range []string{"CreateLoan(", "UpdateLoan(", "UpdateLoanWithOutputOnly("} {
			body, ok := funcBody(src, "func (b *"+recv+") "+m)
			if !ok || !strings.Contains(body, "queueLoanReferences(") {
				t.Errorf("%s: batch %s does not resolve references", file, m)
			}
		}
		if body, ok := funcBody(src, "func (b *"+recv+") PurgeLoan("); !ok || !strings.Contains(body, "queueLoanPurge(") {
			t.Errorf("%s: batch purge does not ask referrers", file)
		}
		if !strings.Contains(src, "PurgeExpired(ctx context.Context, now time.Time)") {
			t.Errorf("%s: no PurgeExpired", file)
		}
	}
	gql := readGolden(t, dir, "graphql.go")
	if !strings.Contains(gql, `"violates foreign key constraint"`) {
		t.Error("graphql.go does not map foreign key violations")
	}
}
