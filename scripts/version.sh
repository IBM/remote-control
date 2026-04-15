#!/usr/bin/env bash

# Get the nearest git tag in the form vX.Y.Z
# and produce a docker tag string with optional commit/dirty status.

# Get the tag in the format vX.Y.Z
tag=$(git describe --tags --abbrev=0 2>/dev/null || echo "")

if [[ -z "$tag" ]]; then
  echo "No tags found. Using 0.0.0." 1>&2
  tag="v0.0.0"
fi

# Extract semantic version (vX.Y.Z)
version=$(echo "$tag" | sed 's/^v//')

# Get current git commit
commit=$(git rev-parse --short HEAD)

# Check if working tree is clean
if [[ -n $(git status --porcelain) ]]; then
  dirty_status="-dirty"
fi

# Check if we are exactly at the tag (no suffix needed)
# We compare if HEAD is a parent of the tag
if git merge-base --is-ancestor "$tag" HEAD && git merge-base --is-ancestor HEAD "$tag"; then
  exact_match=true
else
  exact_match=false
fi

# Construct the docker tag
if $exact_match && [[ -z "$dirty_status" ]]; then
  # Exact match with clean working tree
  docker_tag="$version"
elif $exact_match && [[ -n "$dirty_status" ]]; then
  # Exact match but with changes
  docker_tag="${version}${dirty_status}"
else
  # Not at exact tag, include commit and possibly dirty status
  docker_tag="${version}-${commit}${dirty_status}"
fi

echo "$docker_tag"