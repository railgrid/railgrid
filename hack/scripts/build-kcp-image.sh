#!/usr/bin/env bash
# Build the kcp image `make tilt-cluster` runs every kcp component from
# (RootShard, Shard, FrontProxy + proxy) and load it into the kind cluster.
#
# railgrid runs the kcp fork its hub links, not an upstream release: the fork
# carries the global validating admission webhook the metering enforcer sits
# behind. The image is built from the fork's own Dockerfile, so it holds the
# same binaries a release image does (/kcp, /kcp-front-proxy, ...), and is
# cached per commit: a second run with the same source only checks that the
# image is in Docker and in the kind node.
#
#   build-kcp-image.sh <kcp-dir> <source> <kind-cluster> [image-repo] [fetch-url]
#
#   kcp-dir       a kcp checkout; its git objects are where a <source> ref is
#                 read from (git archive), so a fork commit only has to be
#                 fetched, not checked out
#   source        a git ref in <kcp-dir> (a commit, branch or tag) — or a
#                 directory, in which case that working tree is built as it
#                 stands (tracked and untracked-but-not-ignored files), for
#                 iterating on kcp itself
#   kind-cluster  the kind cluster to load the image into (its control-plane
#                 node must exist)
#   image-repo    the image name (default railgrid-kcp); the tag is the
#                 commit's 12-hex prefix, plus -wt-<fingerprint> for a dirty
#                 working tree
#   fetch-url     where to fetch <source> from if <kcp-dir> does not have it
#                 (the module URL of the go.mod replace); only used on a miss
#
# Prints the image tag on stdout; everything else goes to stderr.
set -euo pipefail

log() { printf 'build-kcp-image: %s\n' "$*" >&2; }
die() { log "$*"; exit 1; }

[ $# -ge 3 ] || die "usage: $0 <kcp-dir> <source> <kind-cluster> [image-repo] [fetch-url]"
kcp_dir=$1
source=$2
cluster=$3
repo=${4:-railgrid-kcp}
fetch_url=${5:-}

[ -d "$kcp_dir/.git" ] || [ -f "$kcp_dir/.git" ] || die "$kcp_dir is not a git checkout"
node="${cluster}-control-plane"
docker inspect "$node" >/dev/null 2>&1 || die "kind cluster $cluster has no node $node"

context_cmd=""
if [ -d "$source" ]; then
  # A working tree: the tag names its HEAD and, when it is dirty, a
  # fingerprint of the diff and the untracked files, so an edit changes the
  # tag and the next run rebuilds instead of trusting the cache.
  src_dir=$(cd "$source" && pwd -P)
  head=$(git -C "$src_dir" rev-parse --short=12 HEAD)
  tag=$head
  if [ -n "$(git -C "$src_dir" status --porcelain)" ]; then
    fingerprint=$({
      git -C "$src_dir" diff HEAD
      git -C "$src_dir" ls-files --others --exclude-standard | LC_ALL=C sort | while IFS= read -r f; do
        printf '%s\n' "$f"
        cat "$src_dir/$f"
      done
    } | shasum -a 256 | cut -c1-8)
    tag="$head-wt-$fingerprint"
  fi
  # Tracked plus untracked-but-not-ignored files that exist on disk, so a
  # deleted file does not abort the archive and bin/ never ends up in it.
  context_cmd="git -C \"$src_dir\" ls-files -z --cached --others --exclude-standard | while IFS= read -r -d '' f; do [ -e \"$src_dir/\$f\" ] && printf '%s\\0' \"\$f\"; done | tar -C \"$src_dir\" --null -T - -cf -"
  log "source: working tree $src_dir at $head$( [ "$tag" != "$head" ] && printf ' (dirty)')"
else
  sha=$(git -C "$kcp_dir" rev-parse --verify --quiet "$source^{commit}" || true)
  if [ -z "$sha" ] && [ -n "$fetch_url" ]; then
    log "$source is not in $kcp_dir; fetching it from $fetch_url"
    git -C "$kcp_dir" fetch --quiet "$fetch_url" "$source" || true
    sha=$(git -C "$kcp_dir" rev-parse --verify --quiet "$source^{commit}" || true)
  fi
  [ -n "$sha" ] || die "commit $source not found in $kcp_dir${fetch_url:+ (nor at $fetch_url)}; fetch the fork there, or point --kcp-dir at a checkout that has it"
  tag=$(git -C "$kcp_dir" rev-parse --short=12 "$sha")
  context_cmd="git -C \"$kcp_dir\" archive --format=tar \"$sha\""
  log "source: commit $sha in $kcp_dir"
fi

image="$repo:$tag"
if docker image inspect "$image" >/dev/null 2>&1; then
  log "image $image is already built"
else
  log "building $image (the first build of a commit compiles kcp; later ones reuse Docker's layer cache)"
  # The context is a tar on stdin, so nothing outside the chosen files or
  # commit (bin/, a stale .kcp/) can leak into the image.
  sh -c "$context_cmd" | DOCKER_BUILDKIT=1 docker build --tag "$image" - >&2
fi

# containerd names a local image docker.io/library/<repo>:<tag>.
if docker exec "$node" crictl inspecti "docker.io/library/$image" >/dev/null 2>&1 || docker exec "$node" crictl inspecti "$image" >/dev/null 2>&1; then
  log "image $image is already in kind cluster $cluster"
else
  log "loading $image into kind cluster $cluster"
  kind load docker-image "$image" --name "$cluster" >&2
fi
printf '%s\n' "$tag"
