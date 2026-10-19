"""difftest driver: run upstream heic-lossless-rotate Python functions in
batch for the Go differential suite (difftest/harness.go).

Usage: python3 -P pydriver.py <requests.json> <out_dir>

requests.json is a list of {"id", "op", "args", "bytes_args"} objects.
"args" are passed as keyword arguments as-is; "bytes_args" are base64-
decoded to bytes first. For each request, writes <out_dir>/<id>.bin with
the op's bytes result, or <out_dir>/<id>.err with "<ExceptionType>: <msg>"
if the op raised. Unknown ops abort the whole batch (exit 2) rather than
being recorded as a per-request error.

Imports come from PYTHONPATH (the harness sets it to the pinned upstream
checkout's src/ and tests/). Standard library only.
"""
import base64
import json
import os
import sys


def _op_synth(kwargs):
    from synth_heic import build_synthetic_heic
    return build_synthetic_heic(**kwargs)


OPS = {
    'synth': _op_synth,
}


def main(req_path, out_dir):
    with open(req_path, encoding='utf-8') as f:
        reqs = json.load(f)

    unknown = sorted({r['op'] for r in reqs} - OPS.keys())
    if unknown:
        print(f"pydriver: unknown op(s): {', '.join(unknown)}", file=sys.stderr)
        sys.exit(2)

    for req in reqs:
        kwargs = dict(req.get('args') or {})
        for key, value in (req.get('bytes_args') or {}).items():
            kwargs[key] = base64.b64decode(value)
        try:
            result = OPS[req['op']](kwargs)
        except Exception as e:  # recorded for the Go side to compare
            with open(os.path.join(out_dir, req['id'] + '.err'), 'w',
                      encoding='utf-8') as f:
                f.write(f"{type(e).__name__}: {e}")
            continue
        with open(os.path.join(out_dir, req['id'] + '.bin'), 'wb') as f:
            f.write(result)


if __name__ == '__main__':
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    main(sys.argv[1], sys.argv[2])
