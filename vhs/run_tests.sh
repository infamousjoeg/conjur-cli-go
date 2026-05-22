#!/bin/bash
# vhs/run_tests.sh

set -euo pipefail
shopt -s nullglob

# Create output directory if it doesn't exist
GOLDEN_DIR="golden"
OUTPUT_DIR="output"
FAILED=0

mkdir -p "$OUTPUT_DIR"

# Function to run a single tape file
run_tape() {
  local tape_file="$1"
  local base_name
  base_name=$(basename "$tape_file" .tape)
  echo "Running VHS tape: $base_name"

  # Run and compare up to 5 times if diff fails
  local attempt=1
  local max_attempts=5
  local success=0

  while [ $attempt -le $max_attempts ]; do
    vhs "$tape_file"
    uniq_frames "$OUTPUT_DIR/${base_name}.txt"
    if diff -u --ignore-all-space --ignore-blank-lines --ignore-matching-lines=">" --ignore-matching-lines="─*" "$GOLDEN_DIR/${base_name}.txt" "$OUTPUT_DIR/${base_name}.txt" > "$OUTPUT_DIR/${base_name}.diff"; then
      success=1
      break
    else
      echo "Attempt $attempt/$max_attempts: Differences found for $base_name"
      sleep 1
      attempt=$((attempt + 1))
    fi
  done

  # Check if the output matched the golden file after all attempts
  if [ $success -eq 1 ]; then
    echo "✅ $base_name matches golden file"
    rm -f "$OUTPUT_DIR/${base_name}.diff"
  else
    echo "❌ Differences found for $base_name after $max_attempts attempts (see ${base_name}.diff)"
    cat "$OUTPUT_DIR/${base_name}.diff"
    return 1
  fi
}

# Check if specific tape names were provided as arguments
if [ $# -gt 0 ]; then
  # Run only the specified tape files
  for tape_name in "$@"; do
    # Add .tape extension if not provided
    if [[ "$tape_name" != *.tape ]]; then
      tape_name="${tape_name}.tape"
    fi

    # Check if the tape file exists
    if [ -f "$tape_name" ]; then
      if ! run_tape "$tape_name"; then
        FAILED=1
      fi
    else
      echo "❌ Tape file not found: $tape_name"
      FAILED=1
    fi
  done
else
  # Clean output dir
  rm -rf "${OUTPUT_DIR:?}"/*

  tape_files=(tapes/*.tape)
  if [ ${#tape_files[@]} -eq 0 ]; then
    echo "❌ No tape files found"
    exit 1
  fi

  # Run all tape files when no parameters are provided
  pids=()
  for tape_file in "${tape_files[@]}"; do
    run_tape "$tape_file" &
    pids+=("$!")
  done

  for pid in "${pids[@]}"; do
    if ! wait "$pid"; then
      FAILED=1
    fi
  done
fi

exit $FAILED