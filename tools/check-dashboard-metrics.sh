#!/usr/bin/env bash
set -euo pipefail

# Proves that every metric name referenced in a Grafana dashboard or
# Prometheus rule under observability/dashboards/ is also emitted by
# Go code somewhere under the workspace. Catches dashboard drift after
# a metric rename or removal — without this gate, a renamed collector
# would silently break the dashboard at scrape time.
#
# Syntactic gate (literal-string match in *.go), not a runtime registry
# check, because the latter requires importing every metrics package
# into one binary. This is strict enough to fail on real drift while
# avoiding a transitive go.mod that depends on every workspace module.
#
# Extends `make check-dashboards` (JSON/YAML syntax) and fills the
# C-004 gap codex round-3 flagged.

dashboards_dir="observability/dashboards"

if [[ ! -d "$dashboards_dir" ]]; then
  echo "missing $dashboards_dir" >&2
  exit 1
fi

allow_file="$(mktemp)"
candidates_file="$(mktemp)"
literals_file="$(mktemp)"
trap 'rm -f "$allow_file" "$candidates_file" "$literals_file"' EXIT

# Allowlist: PromQL builtins, dashboard-template names, and Prometheus
# scrape-time labels that are not metric names but are valid PromQL
# tokens.
cat > "$allow_file" <<'EOF'
abs
absent
absent_over_time
and
avg
avg_over_time
backend
bool
bottomk
broker
by
ceil
changes
clamp
clamp_max
clamp_min
client
cluster
code
command
consumer
container
count
count_over_time
day_of_month
day_of_week
day_of_year
days_in_month
delta
deriv
direction
endpoint
event
exp
exported_instance
exported_namespace
exported_service
floor
group
group_left
group_right
histogram_avg
histogram_count
histogram_fraction
histogram_quantile
histogram_stddev
histogram_stdvar
histogram_sum
holt_winters
hour
idelta
if
ignoring
increase
instance
irate
job
kind
label_join
label_replace
last_over_time
le
ln
log10
log2
max
max_over_time
method
min
min_over_time
minute
month
namespace
node
offset
operation
on
or
path
phase
pod
predict_linear
present_over_time
producer
quantile
quantile_over_time
queue
rate
reason
remote
resets
result
round
route
scalar
server
service
sgn
sort
sort_by_label
sort_by_label_desc
sort_desc
source
sqrt
status
status_code
stddev
stddev_over_time
stdvar
stdvar_over_time
stream
sum
sum_over_time
target
time
timestamp
topic
topk
type
unless
up
vector
without
year

# Grafana dashboard template variable names (referenced as
# instance=~"$storage_instance" inside expressions; the regex
# extraction picks them up as tokens but they are not metric names).
storage_instance

# Prometheus internal labels and alert annotation fields. __name__ is
# the metric-name pseudo-label; runbook_url is the standard alert
# annotation linking to operator runbooks.
name__
runbook_url

# DBStats counters from database/sql exposed by sqlstats — referenced
# in dashboards as foo_db_in_use_connections etc. but the bare
# "in_use" appears as a token after the prefix is stripped by selector
# regexes like __name__=~".+_db_in_use_connections".
in_use

# HTTP RED metric names (observability/redmetrics): the kit composes
# these at runtime from cfg.namespace (empty default, operator-supplied)
# + Subsystem "http" + Name "*_total" so the awk extractor cannot
# resolve them statically. These are part of the v2 frozen public API
# and live in observability/redmetrics/redmetrics.go.
http_requests_total
http_errors_total
http_request_duration_seconds

# gRPC RED metric names emitted by github.com/grpc-ecosystem/go-grpc-prometheus
# (used via grpcx). External-library names that the kit re-exports.
grpc_server_handled_total
grpc_server_handled
grpc_server_handling_seconds
grpc_server_handling_seconds_bucket
EOF
sort -u -o "$allow_file" "$allow_file"

# Extract identifier candidates from PromQL expressions. Grafana JSON
# carries them in `"expr": "..."` fields; Prometheus rule YAML carries
# them under `expr: |` blocks. We grep aggressively and let the
# allowlist filter out non-metric tokens. Capture anything that looks
# like a Prometheus metric name (lowercase + digits + underscores, at
# least one underscore to distinguish from PromQL keywords).
{
  # Grafana: pull only the `"expr": "..."` JSON values.
  find "$dashboards_dir/grafana" -name '*.json' -print0 |
    xargs -0 awk '
      /"expr"[[:space:]]*:[[:space:]]*"/ {
        # Strip everything before "expr": "
        sub(/.*"expr"[[:space:]]*:[[:space:]]*"/, "")
        # Strip the trailing ",\n quote (keep simple: drop after last unescaped quote).
        sub(/",[[:space:]]*$/, "")
        sub(/"$/, "")
        print
      }
    '
  # Prometheus rule YAML: dump every line; the allowlist will filter.
  find "$dashboards_dir/prometheus" -name '*.yaml' -print0 |
    xargs -0 cat
} |
  grep -oE '[a-z][a-z0-9_]+_[a-z0-9_]+' |
  sort -u > "$candidates_file"

candidate_count=$(wc -l < "$candidates_file" | tr -d ' ')

# Collect emitted metric names from Go sources. We need three views:
#
#   1. Bare string literals (e.g. const RouteLabel = "queue_size").
#      Cheap to grep, catches metric names declared as named consts.
#
#   2. Composed Namespace+Subsystem+Name. Prometheus collectors are
#      constructed via prometheus.XxxOpts{ Namespace: "x", Subsystem:
#      "y", Name: "z" } and the runtime metric name is "x_y_z" (the
#      composed name never appears as a single literal). We compose it
#      here by tracking the most-recent Namespace/Subsystem near each
#      Name within the same .go file.
#
#   3. Prometheus recording-rule names from rule YAML. Synthetic
#      metrics defined under `- record: foo` are emitted by the rules
#      engine, not by Go code, and the dashboards legitimately
#      reference them.
#
# We also add a derived-suffix pass: histogram and summary collectors
# auto-expose _bucket / _count / _sum so we treat a base name in code
# as also emitting those three derived series.
{
  find . -name '*.go' \
      -not -name '*_test.go' \
      -not -path './vendor/*' \
      -not -path './node_modules/*' \
      -print0 |
    xargs -0 grep -hoE '[a-z][a-z0-9_]+_[a-z0-9_]+'

  find . -name '*.go' \
      -not -name '*_test.go' \
      -not -path './vendor/*' \
      -not -path './node_modules/*' \
      -print0 |
    xargs -0 awk '
      BEGIN { current_ns=""; current_sub="" }
      FNR == 1 { current_ns=""; current_sub="" }
      /Namespace:[[:space:]]*"/ {
        ns=$0
        sub(/.*Namespace:[[:space:]]*"/, "", ns)
        sub(/".*$/, "", ns)
        current_ns=ns
        next
      }
      # Non-literal namespace (e.g. cfg.namespace): we have no idea
      # what the runtime value is, so reset.
      /Namespace:[[:space:]]*[^"]/ {
        current_ns=""
        next
      }
      /Subsystem:[[:space:]]*"/ {
        sb=$0
        sub(/.*Subsystem:[[:space:]]*"/, "", sb)
        sub(/".*$/, "", sb)
        current_sub=sb
        next
      }
      /Subsystem:[[:space:]]*[^"]/ {
        current_sub=""
        next
      }
      /Name:[[:space:]]*"/ {
        nm=$0
        sub(/.*Name:[[:space:]]*"/, "", nm)
        sub(/".*$/, "", nm)
        # Compose from whatever non-empty parts we have.
        parts=""
        if (current_ns != "") parts=current_ns
        if (current_sub != "") {
          if (parts != "") parts=parts "_" current_sub
          else parts=current_sub
        }
        if (parts != "") full=parts "_" nm
        else full=nm
        print full
        print full "_bucket"
        print full "_count"
        print full "_sum"
        # Reset so the next Opts block does not inherit stale prefix
        # state when its Namespace/Subsystem is a variable.
        current_ns=""
        current_sub=""
      }
    '

  # Recording-rule names declared under prometheus/*.yaml. Each
  # `- record: name(:suffix)*` line declares a synthetic metric the
  # rules engine will emit. Allow both the bare prefix and the full
  # rule name so dashboards can reference either spelling.
  find "$dashboards_dir/prometheus" -name '*.yaml' -print0 |
    xargs -0 awk '
      /^[[:space:]]*-[[:space:]]*record:[[:space:]]*/ {
        name=$0
        sub(/^[[:space:]]*-[[:space:]]*record:[[:space:]]*/, "", name)
        sub(/[[:space:]]*$/, "", name)
        sub(/^["'"'"']/, "", name)
        sub(/["'"'"']$/, "", name)
        print name
        bare=name
        sub(/:.*$/, "", bare)
        if (bare != name) print bare
      }
    '
} | sort -u > "$literals_file"

# A candidate is "missing" if it is not in the allowlist AND not in
# the Go literal set AND not a suffix of any known metric name. The
# suffix check handles selector-regex syntax like
# __name__=~"storage_(s3|gcs|azure)_operation_duration_seconds_count"
# which legitimately references storage_s3_operation_duration_seconds_count
# etc. but the regex extracts the bare tail.
missing=()
while IFS= read -r name; do
  [[ -n "$name" ]] || continue
  if grep -Fxq "$name" "$allow_file"; then
    continue
  fi
  if grep -Fxq "$name" "$literals_file"; then
    continue
  fi
  # Suffix match: does any emitted metric end with _<candidate>?
  if grep -Fq "_$name" "$literals_file"; then
    continue
  fi
  missing+=("$name")
done < "$candidates_file"

if (( ${#missing[@]} > 0 )); then
  echo "dashboard metric drift: the following names appear in a dashboard or rule but are not emitted by any non-test Go source:" >&2
  printf '  %s\n' "${missing[@]}" >&2
  echo
  echo "Either fix the dashboard reference, rename the collector, or update the allowlist in tools/check-dashboard-metrics.sh if the identifier is not a metric name." >&2
  exit 1
fi

allowlist_hits=$(comm -12 "$candidates_file" "$allow_file" | wc -l | tr -d ' ')
echo "dashboard metric contract OK (${candidate_count} candidates; ${allowlist_hits} resolved by allowlist, rest matched against Go sources)"
