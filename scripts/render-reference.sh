#!/usr/bin/env bash
set -euo pipefail

# Capture legacy PNGs from b62f863 using frozen fixture source from 9679327.
# Reference inputs stay independent of newer rendering payload fields. The
# temporary checkout is discarded; only the requested output directory changes.
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
# These fixtures predate semester header fields and reproduce the original
# reference inputs. Keep this revision pinned alongside the renderer revision.
git -C "$repo" archive 967932782ccda31aefe6d8405e860dc5cb2e5426 cmd/render-examples | tar -x -C "$tmp_repo"

# The legacy Renderer did not yet expose a clock hook. Add the narrow hook to
# this temporary checkout so date-sensitive emphasis and footer text are
# deterministic.
perl -0pi -e 's/type Renderer struct \{\n\tFontPath string\n\}/type Renderer struct {\n\tFontPath string\n\tNow func() time.Time\n}\n\nfunc (r Renderer) now() time.Time {\n\tif r.Now != nil {\n\t\treturn r.Now()\n\t}\n\treturn time.Now()\n}/' "$tmp_repo/internal/responses/render.go"
for source in rich_render.go schedule_grid.go weather_render.go; do
	perl -0pi -e 's/time\.Now\(\)\.In\(time\.FixedZone\("CST", 8\*60\*60\)\)/r.now().In(time.FixedZone("CST", 8*60*60))/g' "$tmp_repo/internal/responses/$source"
done

# Use the old in-process renderer, and permit its natural dimensions (the
# fixture command's checks describe the Typst sheet).
perl -0pi -e 's/responses\.RemoteRenderer\{Endpoint: endpoint, Now: fixtureNow\}/responses.Renderer{Now: fixtureNow}/' "$tmp_repo/cmd/render-examples/main.go"
perl -0pi -e 's/exampleMinWidth\s*=.*/exampleMinWidth  = 1/' "$tmp_repo/cmd/render-examples/main.go"
perl -0pi -e 's/exampleMaxWidth\s*=.*/exampleMaxWidth  = 100000/' "$tmp_repo/cmd/render-examples/main.go"
perl -0pi -e 's/exampleMinHeight\s*=.*/exampleMinHeight = 1/' "$tmp_repo/cmd/render-examples/main.go"

cd "$tmp_repo"
go run ./cmd/render-examples -out "$output_dir"
