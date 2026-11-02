#!/usr/bin/env bash
# Merge the latest upstream server and panel releases into a new branch.
#
#   scripts/sync-upstream.sh [--dry-run] [--no-test] [--server vX.Y.Z] [--panel vX.Y.Z]
#
# Exit codes: 0 merged (or nothing to do), 2 conflicts, 3 tests failed, 1 other
# errors. Conflicted files are written to $SYNC_CONFLICTS_FILE when it is set. A
# conflicting merge is aborted, leaving the branch at its last good state.
#
# SERVER_REPO and PANEL_REPO override the upstream URLs, for testing.
set -euo pipefail

SERVER_REPO="${SERVER_REPO:-https://github.com/router-for-me/CLIProxyAPI.git}"
PANEL_REPO="${PANEL_REPO:-https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git}"

dry_run=false
run_tests=true
server_tag=""
panel_tag=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--dry-run) dry_run=true ;;
	--no-test) run_tests=false ;;
	--server)
		server_tag="$2"
		shift
		;;
	--panel)
		panel_tag="$2"
		shift
		;;
	*)
		echo "unknown option: $1" >&2
		exit 1
		;;
	esac
	shift
done

cd "$(git rev-parse --show-toplevel)"

recorded() {
	awk -F'|' -v name="$1" '$2 ~ name { gsub(/ /, "", $4); print $4 }' UPSTREAM.md
}

latest_tag() {
	git ls-remote --tags --refs "$1" 'v*' |
		sed 's#.*refs/tags/##' |
		grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' |
		sort -V |
		tail -n 1
}

current_server="$(recorded Server)"
current_panel="$(recorded Panel)"
[ -n "$server_tag" ] || server_tag="$(latest_tag "$SERVER_REPO")"
[ -n "$panel_tag" ] || panel_tag="$(latest_tag "$PANEL_REPO")"

echo "Server: $current_server -> $server_tag"
echo "Panel:  $current_panel -> $panel_tag"

if [ "$server_tag" = "$current_server" ] && [ "$panel_tag" = "$current_panel" ]; then
	echo "Already up to date."
	exit 0
fi
if "$dry_run"; then
	exit 0
fi
if ! git diff --quiet || ! git diff --cached --quiet; then
	echo "The working tree has changes. Commit them first." >&2
	exit 1
fi

report_conflicts() {
	local source="$1"
	local files
	files="$(git diff --name-only --diff-filter=U)"
	echo "Conflicts merging $source:" >&2
	echo "$files" >&2
	if [ -n "${SYNC_CONFLICTS_FILE:-}" ]; then
		{
			echo "Merging $source conflicted in:"
			echo "$files"
		} >>"$SYNC_CONFLICTS_FILE"
	fi
	git merge --abort
	exit 2
}

branch="upstream-sync/server-$server_tag-panel-$panel_tag"
git switch -c "$branch"
echo "branch=$branch" >>"${GITHUB_OUTPUT:-/dev/null}"

if [ "$server_tag" != "$current_server" ]; then
	git fetch --quiet --no-tags "$SERVER_REPO" "refs/tags/$server_tag:refs/tags/upstream-$server_tag"
	git merge --no-edit -m "Merge upstream server $server_tag" "upstream-$server_tag" ||
		report_conflicts "server $server_tag"
fi

if [ "$panel_tag" != "$current_panel" ]; then
	git fetch --quiet --no-tags "$PANEL_REPO" "refs/tags/$panel_tag:refs/tags/panel-upstream-$panel_tag"
	git subtree merge --prefix=panel --squash -m "Merge upstream panel $panel_tag" "panel-upstream-$panel_tag" ||
		report_conflicts "panel $panel_tag"
fi

sed -i.bak -E \
	-e "s/^(\| Server \|[^|]*\| )v[0-9.]+( \|)$/\1$server_tag\2/" \
	-e "s/^(\| Panel \|[^|]*\| )v[0-9.]+( \|)$/\1$panel_tag\2/" \
	UPSTREAM.md
rm -f UPSTREAM.md.bak
sed -i.bak -E \
	-e "s/(UpstreamVersion = )\"v[0-9.]+\"/\1\"$server_tag\"/" \
	-e "s/(PanelVersion    = )\"v[0-9.]+\"/\1\"$panel_tag\"/" \
	internal/tpr/update/release.go
rm -f internal/tpr/update/release.go.bak
git add UPSTREAM.md internal/tpr/update/release.go
git commit -q -m "Record upstream server $server_tag and panel $panel_tag"

if "$run_tests"; then
	if ! go vet ./internal/tpr/... ./cmd/server || ! go test ./internal/tpr/... ./internal/managementasset ./internal/api/...; then
		echo "Tests failed on $branch." >&2
		exit 3
	fi
fi

echo "Merged into $branch. Review it, then open a pull request."
