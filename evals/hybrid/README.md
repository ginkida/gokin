# Hybrid Engine Evals

This suite measures the narrow workload for which the read-only computation
plane is intended. It is separate from `evals/coding` so adding analytics cases
does not invalidate provider coding baselines.

The suite contains six collection-scale positive cases over three different
fixtures and two targeted negative controls: an explicitly named file pair and
a single-file count. Workloads cover marker aggregation, bounded evidence,
cross-file declaration/reference joins, mixed-language inventory, and parsed
JSON joins rather than repeating one synthetic query shape. Every fixture
starts green, must remain unchanged, and has a machine-checked answer.
`hybrid_policy_expected` also verifies that `auto` offers the REPL only to the
positive cases; explicit `tools` and `hybrid` modes remain valid overrides.
All six positive cases additionally require runtime proof of their one-pass path:
`count_code_many`, count-with-`sample_limit`, streaming `file_stats`, or
ignore-aware `list_files` when the actual paths are needed.
The combined-marker case accepts either a single regex `count_code` or one
`count_code_many` scan; equivalent one-pass implementations are not penalized
merely for choosing a different primitive.
Merely exposing or invoking `repl_exec` does not satisfy
`hybrid_efficient_path`. Those cases also cap collection scans and REPL calls
at one, so invoking the preferred primitive after redundant work still fails.
They require one repository-index refresh observed by the Go parent, so
missing worker-to-parent scan evidence fails instead of being treated as an
efficient run.
The eval runner stores this journal in a private sibling runtime directory,
outside the model-writable fixture workspace. Workspace journal lookalikes are
ignored, and the runtime path is withheld from model bash/REPL and verification
environments.

Validate the fixture contract without a provider call:

Manifest loading fails closed on unknown fields, duplicate JSON keys at any
depth, and trailing JSON documents, so a misspelled efficiency gate cannot be
silently ignored.

```sh
go run ./cmd/gokin eval validate \
  --manifest evals/hybrid/manifest.json \
  --fixtures evals/hybrid/fixtures
```

Run a three-way comparison with the exact binary under test:

```sh
go build -o /tmp/gokin ./cmd/gokin
GOKIN_BIN=/tmp/gokin go run ./cmd/gokin eval run \
  --manifest evals/hybrid/manifest.json \
  --fixtures evals/hybrid/fixtures \
  --provider glm --model glm-5.2 \
  --engine-mode tools --engine-mode auto --engine-mode hybrid \
  --repeat 3 \
  --agent-command "$(pwd)/evals/coding/scripts/run-gokin-headless.sh" \
  --output .gokin/evals/hybrid-engine-ab.jsonl

go run ./cmd/gokin eval report \
  --input .gokin/evals/hybrid-engine-ab.jsonl \
  --require-pass \
  --fail-metric hybrid_policy_expected=100% \
  --fail-metric hybrid_efficient_path=100% \
  --require-complete-engine-pairs \
  --max-engine-score-regression 0 \
  --max-engine-quality-regressions 0 \
  --max-engine-relative-delta candidates.total_tokens=0% \
  --max-engine-relative-delta candidates.input_tokens=0% \
  --max-engine-median-relative-delta candidates.total_tokens=-5% \
  --max-engine-relative-delta candidates.model_rounds=0% \
  --max-engine-relative-delta controls.total_tokens=5% \
  --max-engine-relative-delta controls.input_tokens=5% \
  --min-engine-lower-ratio candidates.total_tokens=50% \
  --max-engine-lower-p-value candidates.total_tokens=5% \
  --min-engine-repl-use-ratio candidates=50% \
  --max-engine-repl-use-ratio controls=0%
```

The runner checkpoints each completed provider row to
`.gokin/evals/hybrid-engine-ab.jsonl.partial` without replacing the previous
complete report. If this long matrix is interrupted, rerun the same command
with `--resume`; exact provenance checks reject changed inputs before making
another provider call, and the completed prefix is not billed twice.

Use `Paired engine deltas vs tools` as the primary result. Each delta is formed
only from the same scenario, provider, model, fault profile, and scenario spec;
the report lists any rows excluded from pairing instead of mixing cohorts.
`candidates` measures the collection-scale cases where the REPL should help,
while `controls` exposes overhead on pairwise and single-file work where it
should not.

A useful hybrid result has a non-negative pass/quality delta and reduces model
rounds, tokens, or duration on candidates. Negative efficiency deltas are
better. Each paired metric shows `tools average → current average`, mean and
pair-median absolute/relative deltas, clustered-unit medians, and
`lower/equal/higher` counts. It also reports a one-sided exact sign-test p-value
over trial-clustered scenario/provider/model/fault evidence units.
Under the sign test, those units are the independence assumption; the result is
evidence for this suite, not proof of universal workload performance. Repeated trials
are averaged inside their unit before its direction is counted, so increasing
`--repeat` reduces noise but cannot manufacture statistical confidence. Tied
units are excluded from the exact test and remain visible in the unit counts.
Use these signals to reject an apparent average win caused by one outlier. The
aggregate
`Engine efficiency` section remains useful for capacity and adoption checks,
but is not a substitute for the paired comparison. Its
`policy → mode/mismatch → strategy → aligned/gaps/unexpected → eligible → exposed → used → repl calls → efficient path`
funnel keeps mode provenance, classification evidence, per-row schema
consistency, model choice, and invocations separate. An eligible-but-unexposed row is an
availability/configuration gap; an ineligible-but-exposed row is a policy leak.
They are counted per row, so opposite errors cannot cancel just because the
eligible and exposed totals happen to be equal. An exposed-but-unused row is an
adoption signal, not automatically a quality failure. Required runtime-operation
counters appear beside the funnel and make batched/one-pass adoption auditable
without retaining cell code. New runtimes emit a worker-owned `file_inventory`
counter at the lowest shared inventory layer, so same-cell snapshot replays and
introspective private-helper calls cannot under-report scan work. Reports use
that authoritative counter when present and fall back to public calls
(`count_code`, `count_code_many`, `search_code`, `file_stats`, and `list_files`)
for legacy JSONL; sampled-mode markers do not double-count the underlying scan.
`index refreshes` is stronger parent-observed evidence from the worker protocol,
so it cannot be supplied by the Python result payload. A compound cell may
share one bounded snapshot for repeated scans of the same scope; the cache is
cleared between cells and before mutable orchestration callbacks. REPL adoption
by itself is not success. Strategy counts (`aggregation`, `cross_file`, or
`explicit`) record the request-specific hint actually sent to the model.
`hybrid_policy_expected` fails when a candidate receives a strategy that does
not match the manifest prompt, so these counts are provenance rather than
decorative labels.

`--repeat 3` creates three isolated trials of every matrix row. The runner keeps
each comparable `provider/model/fault` cohort together, cyclically rotates the
cohort order, and counterbalances engine order inside every cohort.
With three engine modes and three trials, each mode therefore occupies every
local execution position once instead of inheriting a fixed warm-cache or load
position. Pairing includes the trial number, so a missing run is reported as
unmatched instead of being compared with another repetition. Increase the
repeat count only deliberately: provider usage grows linearly with it, and the
runner caps it at 100. When first-order carry-over is a material concern, six
trials add the reversed Latin block: `ABC/BCA/CAB/CBA/ACB/BAC`. This balances
both local position and every directed adjacent pair, but does not turn those
repetitions into six independent statistical evidence units.

The engine gates above target production `auto` by default and fail closed on
missing/excluded pairs, absent headless measurements, any aggregate or
candidate/control score loss, missing, wrong-mode, or exposure-inconsistent
hybrid-policy evidence, any individual quality regression, and absent or
malformed SHA-256 scenario/run provenance. Legacy JSONL without hashes remains
readable for diagnostics, but cannot satisfy an engine gate; candidate/control
classification must likewise be present on both sides. The text and JSON
reports show verified provenance counts beside the paired results. Add
`--engine-gate-mode hybrid` to gate explicit hybrid mode too; repeat the flag
when both modes should be enforced.

Efficiency thresholds use `cohort.metric=value`. Supported cohorts are `all`,
`candidates`, and `controls`; metrics are `input_tokens`,
`uncached_input_tokens`, `cache_read_input_tokens`, `output_tokens`,
`total_tokens`, `model_rounds`, `duration_ms`, `repl_calls`, and
`estimated_usd`. A maximum relative delta of `0%` means “must not be worse than
tools”; `-5%` requires at least 5% savings.
`input_tokens` is the complete prompt-side volume and is the direct check for
the hybrid context-compression claim. `uncached_input_tokens` subtracts the
provider-reported cache-read subset, while `output_tokens` exposes a model that
merely moves verbosity into its answer. A lower `cache_read_input_tokens` value
is not intrinsically better: it can mean a smaller prompt or worse cache reuse,
so use it diagnostically unless the workload gives its direction a clear
meaning. Component gates fail closed when either paired row lacks an explicitly
tracked, internally consistent token breakdown; older JSONL remains usable for
`total_tokens` and other available metrics.
The median-relative gate first averages repeated relative deltas inside each
scenario/provider/model/fault evidence unit, then applies the threshold to the
median across those units. It requires every paired baseline to be non-zero.
Thus repeated trials cannot occupy several central ranks, and one very large
scenario cannot pull the practical-effect gate across the threshold.
The lower-ratio gate measures consistency across clustered evidence units, so
`50%` requires the selected engine's average repeated-trial delta to be strictly
lower in at least half of the scenario/provider/model/fault units. Individual
trials remain visible in pair counts but cannot inflate this gate. Relative
deltas fail closed when the tools baseline average is zero; use a different
metric instead of interpreting division by zero as an improvement.
`--max-engine-lower-p-value candidates.total_tokens=5%` is the stronger
consistency gate: it fails unless the clustered one-sided exact sign test has
enough non-tied evidence units and its p-value is at most 0.05. The six
candidate scenarios make that threshold attainable without treating repeated
trials as separate evidence. Select one primary efficiency metric before a
run; gating many metrics at 5% without a multiple-testing correction overstates
the evidence.

REPL-use thresholds use `cohort=ratio`. The denominator is every valid paired
row in that cohort, while the numerator is rows with at least one `repl_exec`
call. A candidate minimum proves that the exposed capability was actually
adopted; a control maximum catches inappropriate use. Both gates fail closed
when the cohort has no pairs, any pair lacks hybrid-policy evidence, reports a
different engine mode, or its eligibility disagrees with the model-visible
schema. Adoption is necessary
evidence for this feature, but quality and efficiency gates still decide
whether that adoption was beneficial.
