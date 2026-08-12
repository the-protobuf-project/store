# Migrating annotations

Two renames have happened to the vocabulary this plugin reads. Both are
mechanical or nearly so, and neither changes what any option *means*: field
numbers, field names, and semantics are identical on the far side. What changes is
which module owns the option, and therefore what you import and what prefix you
write.

| If your protos say | Go to | Effort |
| --- | --- | --- |
| `(protokit.v1.*)` | [Migration A](#migration-a-protokitv1--entityv1) | one command |
| `(orm.v1.*)` | [Migration B](#migration-b-ormv1--entityv1--storev1) | one command + one rule |

You do not need to do both. `orm.v1` predates `protokit.v1`; if you are on
`orm.v1`, Migration B takes you all the way.

Whichever you run, finish with [Verify](#verify) — the plugin will name anything
you missed, by field and by message, so you are never left guessing.

---

## Migration A: `protokit.v1` → `entity.v1`

protokit shipped the neutral vocabulary itself through v1.2.0, then removed it in
**v1.2.1**: the vocabulary is persistence-shaped — datasources, tables, columns —
and protokit generates contracts too, for which none of those exist. It now reads
AIP and nothing else, and the vocabulary lives here as `entity.v1`.

Nothing was renumbered or resemanticised. This is a prefix and an import.

**1. Rewrite the protos.**

```bash
find . -name '*.proto' -exec perl -pi -e '
  s{\bprotokit\.v1\.}{entity.v1.}g;
  s{"protokit/v1/}{"entity/v1/}g;
' {} +
```

**2. Swap the BSR dependency** in `buf.yaml`:

```yaml
deps:
  - buf.build/the-protobuf-project/entity # was: .../protokit
```

**3. Refresh and tidy:**

```bash
buf dep update && buf format -w
```

---

## Migration B: `orm.v1` → `entity.v1` + `store.v1`

`orm.v1` was one vocabulary saying two different kinds of thing: what a table is
*named* and how its columns are *stored*. That is why no other generator could
read it — a cache has every reason to agree with you about a table name and none
at all to care that a column is `VARCHAR(255)`. Splitting it is what lets those
generators compose.

So each `orm.v1` option moves to whichever module owns that kind of statement:

| Was | Is now |
| --- | --- |
| `(orm.v1.datasource)` | `(entity.v1.datasource)` |
| `(orm.v1.table)` — `table`, `skip`, `id`, `timestamps`, `indexes` | `(entity.v1.table)` |
| `(orm.v1.query)` | `(store.v1.query)` |
| `(orm.v1.column)` — `column`, `skip` | `(entity.v1.column)` |
| `(orm.v1.column)` — `type`, `max_length`, `precision`, `scale`, `default_value`, `unique`, `index`, `on_delete`, `on_update` | `(store.v1.column)` |

The first three rows are a pure rename. The last two are the one place a script
cannot finish the job, because a single `(orm.v1.column)` can hold fields bound
for both modules — see [the column split](#the-column-split) below.

**1. Rewrite everything that is unambiguous:**

```bash
find . -name '*.proto' -exec perl -pi -e '
  s{\(orm\.v1\.datasource\)}{(entity.v1.datasource)}g;
  s{\(orm\.v1\.table\)}{(entity.v1.table)}g;
  s{\(orm\.v1\.query\)}{(store.v1.query)}g;
  s{"orm/v1/annotations\.proto"}{"entity/v1/annotations.proto"}g;
' {} +
```

Then add the `store.v1` import to each file that now references it:

```bash
grep -rl 'store\.v1\.' --include='*.proto' . | while read -r f; do
  grep -q 'import "store/v1/annotations.proto";' "$f" ||
    perl -pi -e 's{(import "entity/v1/annotations\.proto";)}{$1\nimport "store/v1/annotations.proto";}' "$f"
done
```

Two steps rather than one because a file whose columns are all structural never
references `store.v1` at all, and an unused import fails `buf lint`. Re-run this
command after step 4, when splitting columns introduces `store.v1` to more files.

**2. Depend on both modules** in `buf.yaml`:

```yaml
deps:
  - buf.build/the-protobuf-project/entity
  - buf.build/the-protobuf-project/store
```

**3. Refresh and tidy:**

```bash
buf dep update && buf format -w
```

**4. Split the remaining `(orm.v1.column)` options**, then [verify](#verify).

### The column split

A field whose `(orm.v1.column)` holds only structural fields, or only physical
ones, is a rename — apply the table above and move on. The case worth showing is
the mixed one, where one option becomes two:

```proto
// Before
string display_label = 3 [(orm.v1.column) = {
  column: "label"        // structural — what it is called
  type: "TEXT"           // physical   — how it is stored
  index: true
}];

// After
string display_label = 3 [
  (entity.v1.column) = {column: "label"},
  (store.v1.column) = {type: "TEXT", index: true}
];
```

Do not attempt this with a regex. The option body spans lines, nests, and the
correct split depends on which fields are present — and a wrong guess here is
quiet: an option sent to the wrong module is simply not read, so the table still
generates, just with the default column name or without the index. Step 4 of
[Verify](#verify) is what finds these, and it names them one by one.

---

## Verify

Your protos keep generating throughout the migration: a compatibility reader
supplies the structural half of `orm.v1` and emits a lint diagnostic per option
rather than failing. That is deliberate — it means you can migrate incrementally
and stay green. It also means **a successful build is not evidence you are
finished.**

To find what is left, promote those diagnostics to errors:

```bash
protoc-gen-store --store_opt=strict=lint:error   # or opt: [strict=lint:error] in buf.gen.yaml
```

Every remaining option is reported by field and by the message or file it sits on:

```
protokit: strict: 9 schema problem(s):
  - [lint] entity.v1-compat sets table on legacyvocab.v1.Widget; that structural
    option is deprecated — these are read for compatibility only; replace them
    with the matching (entity.v1.*) option
  - [lint] entity.v1-compat sets column on legacyvocab.v1.Widget.label; …
  - [lint] entity.v1-compat sets database on schema.proto; …
```

Work the list until it is empty. Note what is *absent* from it: physical options
like `type` and `max_length` are never reported, because `store.v1` still owns
them and they are being read by the live reader, not the compatibility one. So the
list is exactly the set of things that still need to move — including each half of
a mixed column option.

Once it is empty, keep `strict=lint:error` on in CI. The compatibility reader is a
migration aid, not a supported vocabulary, and it is removed after one major
version.

---

## What does not change

- **Field numbers and names.** `id: ID_STRATEGY_ULID` means precisely what it
  meant. No value in any option body needs editing.
- **Generated output.** A migrated proto produces byte-identical output to its
  unmigrated twin. This is not a claim of intent: `testdata/strict/legacy_vocab`
  and `legacy_vocab_twin` are the same schema in the two vocabularies, and
  `TestLegacyVocabularyEquivalence` generates both through every database target
  and requires the bytes to match. The plugin cannot drift on it.
- **Extension numbers.** `entity.v1` occupies 51000–51002 and `store.v1`
  50010–50012, so a half-migrated file carrying both vocabularies is legal and
  generates correctly. That is what makes incremental migration possible.
