#!/usr/bin/env python3
"""
Generate a JSON payload for /put with a random ASCII data string of a target size.
Writes a file like: {"data":"<random>"}
"""

import argparse
import random
import string


def main() -> None:
    parser = argparse.ArgumentParser(description="Generate JSON payload with random data")
    parser.add_argument("--size-bytes", type=int, required=True, help="Size of data string in bytes")
    parser.add_argument("--output", required=True, help="Output JSON file path")
    parser.add_argument("--chunk-bytes", type=int, default=1024 * 1024, help="Chunk size for streaming output")
    parser.add_argument("--seed", type=int, default=None, help="Optional RNG seed for reproducibility")
    args = parser.parse_args()

    if args.size_bytes < 0:
        raise SystemExit("size-bytes must be >= 0")

    if args.seed is not None:
        random.seed(args.seed)

    alphabet = string.ascii_letters + string.digits

    remaining = args.size_bytes
    with open(args.output, "w", encoding="utf-8") as f:
        f.write('{"data":"')
        while remaining > 0:
            chunk = min(remaining, args.chunk_bytes)
            f.write("".join(random.choice(alphabet) for _ in range(chunk)))
            remaining -= chunk
        f.write('"}')


if __name__ == "__main__":
    main()
