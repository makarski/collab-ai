#!/bin/sh
# Install the complete, portable skill bundle into a fresh image or staging tree.
set -eu
source_dir=${1:?Usage: install-skill.sh SOURCE DESTINATION}
destination=${2:?Usage: install-skill.sh SOURCE DESTINATION}
install -d "$destination/references"
install -m 0644 "$source_dir/SKILL.md" "$destination/SKILL.md"
install -m 0644 "$source_dir"/references/*.md "$destination/references/"
