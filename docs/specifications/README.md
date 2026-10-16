# Native semantic contracts

`contracts.json` keeps one atomic requirement bound to one literal Go canonical
subtest. Version 2 adds closed expectation families with named cases. The Go
observation types and `contracts.schema.json` define the allowed fields; there is
no expression language or free-form assertion list. `rationale` and acceptance
prose explain each scenario; only typed expectations define its normative results.

Each existing canonical test calls `specification.Begin(t)`, exercises its real
client/command/MCP/persistence path, and checks a typed observation using
`Check(caseID, observation)`. The checker compares every declared field, rejects
unknown/repeated cases, and fails cleanup if any declared case was not consumed.
It then runs a native case subtest and emits its observed values and consumption
receipt. Expected outcomes live in the contract instead of being copied into the
checker. Fixture inputs remain explicit synthetic data in tests and `fixtures/`.

REST source references select the pinned OpenAPI operation ID, parameter names,
response status, and instance JSON pointers (array index `0` denotes the row
shape). The source checker resolves those references, validates the actual wire
request against its own endpoint's bounds, and validates successful fixture
shapes before serving them. Deliberately malformed responses must fail that
schema validation. Complete collection cases also compare every referenced row
field before and after the generated-model projection. Every source-backed case
records request/fixture validation counts; collection evidence must include the
actual projection fields and matching request count.

The limits are endpoint-specific: the current exam API permits page 100000 and
page size 100, while section and workspace homework APIs permit page 100 and
page size 50. Bot homework requests already use 50. The personal homework
100-page guard remains justified by that operation; it must not be applied to
exam collections. Fixture page totals are coherent: the 101-page exam scenario
contains 5001 records at page size 50, and homework uses 51 records over two pages.

CI runs the complete native race suite with `-json -count=1`. `cmd/spec-evidence`
accepts evidence only from a clean committed checkout, checks the source snapshot
against its provenance, and binds every receipt to the current commit and
SHA-256 hashes of the specification, semantic schema, and OpenAPI source.
Declarations, fully consumed observations, and native package/test run and pass events must all
agree. Each binding uses the module path from `go.mod` and its declared test file
directory, and requires exactly one run and pass for the canonical test and each
case. Split native log chunks are reassembled within their package/test stream.
Missing/duplicate/unknown cases, skipped canonical tests, stale hashes,
unknown/unconsumed fields, wrong observed values, and missing wire validation
fail the report. Other ordinary tests may retain their existing conditional skips.

To reproduce on a committed tree, write artifacts outside the checkout:

```sh
set -o pipefail
go test -race -json -count=1 ./... | tee "$TMPDIR/bot-native-tests.jsonl"
go run ./cmd/spec-evidence \
  -input "$TMPDIR/bot-native-tests.jsonl" \
  -output "$TMPDIR/bot-semantic-evidence.json"
```

CI uploads both files as `bot-semantic-evidence`. Infrastructure guard tests
mutate declarations, outcomes, native events, hashes and source references; they
also prove that schema-invalid success fixtures and out-of-bounds actual
requests are rejected.
