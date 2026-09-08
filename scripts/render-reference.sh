#!/usr/bin/env bash
set -euo pipefail

# Capture legacy PNGs from b62f863 using this checkout's current fixture
# source. The temporary checkout is discarded; only the requested output
# directory is changed.
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$script_dir/.." && pwd)
output_dir=${1:-"$repo/examples/reference"}
mkdir -p "$output_dir"
if [[ "$output_dir" != /* ]]; then
	output_dir=$(CDPATH= cd -- "$(dirname -- "$output_dir")" && pwd)/$(basename -- "$output_dir")
fi

tmp_repo=$(mktemp -d "${TMPDIR:-/tmp}/life-ustc-legacy-render.XXXXXX")
trap 'rm -rf "$tmp_repo"' EXIT

git -C "$repo" archive b62f863fda82f9ddc1002de2ea89555f49eede8a | tar -x -C "$tmp_repo"
if [[ ! -f "$repo/cmd/render-examples/main.go" ]]; then
	echo "fixture source not found: $repo/cmd/render-examples/main.go" >&2
	exit 1
fi

# Copy the fixture command from the current implementation. Its only renderer
# dependency is rewritten below; the fixture literals remain source-identical.
mkdir -p "$tmp_repo/cmd/render-examples"
cp "$repo/cmd/render-examples/main.go" "$tmp_repo/cmd/render-examples/main.go"
if [[ -f "$repo/cmd/render-examples/gallery.go" ]]; then
	cp "$repo/cmd/render-examples/gallery.go" "$tmp_repo/cmd/render-examples/gallery.go"
fi

# The legacy Renderer did not yet expose a clock hook. Add the narrow hook to
# this temporary checkout so date-sensitive emphasis and footer text are
# deterministic.
perl -0pi -e 's/type Renderer struct \{\n\tFontPath string\n\}/type Renderer struct {\n\tFontPath string\n\tNow func() time.Time\n}\n\nfunc (r Renderer) now() time.Time {\n\tif r.Now != nil {\n\t\treturn r.Now()\n\t}\n\treturn time.Now()\n}/' "$tmp_repo/internal/responses/render.go"
for source in rich_render.go schedule_grid.go weather_render.go; do
	perl -0pi -e 's/time\.Now\(\)\.In\(time\.FixedZone\("CST", 8\*60\*60\)\)/r.now().In(time.FixedZone("CST", 8*60*60))/g' "$tmp_repo/internal/responses/$source"
done

# Use the old in-process renderer, and permit its natural dimensions (the
# current command's checks describe the newer fixture sheet).
perl -0pi -e 's/responses\.RemoteRenderer\{Endpoint: endpoint, Now: fixtureNow\}/responses.Renderer{Now: fixtureNow}/' "$tmp_repo/cmd/render-examples/main.go"
perl -0pi -e 's/exampleMinWidth\s*=.*/exampleMinWidth  = 1/' "$tmp_repo/cmd/render-examples/main.go"
perl -0pi -e 's/exampleMaxWidth\s*=.*/exampleMaxWidth  = 100000/' "$tmp_repo/cmd/render-examples/main.go"
perl -0pi -e 's/exampleMinHeight\s*=.*/exampleMinHeight = 1/' "$tmp_repo/cmd/render-examples/main.go"

cd "$tmp_repo"
go run ./cmd/render-examples -out "$output_dir"
