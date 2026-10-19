"""difftest driver: run upstream heic-lossless-rotate Python functions in
batch for the Go differential suite (difftest/harness.go).

Usage: python3 -P pydriver.py <batch.json> <results.jsonl>

batch.json is {"inputs": {name: base64}, "requests": [...]}, where each
request is {"id", "op", "args", "bytes_args", "input_args"}:
  args        keyword arguments passed as-is
  bytes_args  keyword arguments base64-decoded to bytes first
  input_args  keyword arguments naming an entry of "inputs" (shared bytes)

Writes one JSON line per request, in order:
  {"id", "out": base64|null, "json": any|null, "log": str, "err": str|null}
where "log" is everything the op printed to stdout (verbose output) and
"err" is "<ExceptionType>: <message>" if it raised. Each request gets a
timer (its optional "timeout" in seconds, default 5), so an upstream
infinite loop is recorded as TimeoutError instead of hanging the suite.
Unknown ops abort the batch (exit 2).

Imports come from PYTHONPATH (the harness sets it to the pinned upstream
checkout's src/ and tests/). Standard library only.
"""
import base64
import contextlib
import io
import json
import signal
import sys

DEFAULT_TIMEOUT_SECONDS = 5


def _op_synth(kw):
    from synth_heic import build_synthetic_heic
    return {'out': build_synthetic_heic(**kw)}


def _op_apply_rotation(kw):
    from heic_rotate import core
    return {'out': core.apply_rotation(kw['data'], kw['delta_turns'],
                                        sync_exif=kw['sync_exif'], verbose=True)}


def _op_reverse_rotation(kw):
    from heic_rotate import core
    return {'out': core.reverse_rotation(kw['data'], ignore_tamper=kw['ignore_tamper'],
                                          verbose=True)}


def _op_verify_reversible(kw):
    from heic_rotate import core
    core.verify_reversible(kw['data'], ignore_tamper=kw['ignore_tamper'], verbose=True)
    return {}


def _op_info(kw):
    from heic_rotate import core
    info = core.gather_info(kw['data'])
    text, code = core.format_info(info, quiet=False)
    quiet_text, quiet_code = core.format_info(info, quiet=True)
    return {'json': {'info': info, 'text': text, 'code': code,
                     'quiet_text': quiet_text, 'quiet_code': quiet_code}}


def _op_verify_structure(kw):
    from heic_rotate import core
    core.verify_structure(kw['data'])
    return {}


def _op_get_current_rotation(kw):
    from heic_rotate import core
    return {'json': core.get_current_rotation(kw['data'])}


def _op_make_v1_style_record(kw):
    from test_heic_rotate import _make_v1_style_record
    return {'out': _make_v1_style_record(kw['original'], kw['rotated_v2'])}


def _op_strip_provenance_tag(kw):
    from test_heic_rotate import _strip_provenance_tag
    return {'out': _strip_provenance_tag(kw['data'], kw['tag'].encode('ascii'))}


def _op_repr(kw):
    # kind: 'bytes' | 'str' | 'hex08' | 'enumerate' | 'int_bool_list'
    kind, value = kw['kind'], kw.get('value')
    if kind == 'bytes':
        text = repr(kw['raw'])
    elif kind == 'str':
        text = repr(value)
    elif kind == 'decode_ascii_replace':
        text = kw['raw'].decode('ascii', 'replace')
    elif kind == 'hex08':
        text = f"{value:#010x}"
    elif kind == 'enumerate':
        text = str(list(enumerate(value, 1)))
    elif kind == 'int_bool_list':
        text = str([(i, b) for i, b in value])
    else:
        raise ValueError(f"unknown repr kind {kind!r}")
    return {'json': text}


OPS = {
    'synth': _op_synth,
    'apply_rotation': _op_apply_rotation,
    'reverse_rotation': _op_reverse_rotation,
    'verify_reversible': _op_verify_reversible,
    'info': _op_info,
    'verify_structure': _op_verify_structure,
    'get_current_rotation': _op_get_current_rotation,
    'make_v1_style_record': _op_make_v1_style_record,
    'strip_provenance_tag': _op_strip_provenance_tag,
    'repr': _op_repr,
}


def _alarm(signum, frame):
    raise TimeoutError("no result before the request's timeout")


def main(batch_path, results_path):
    with open(batch_path, encoding='utf-8') as f:
        batch = json.load(f)
    inputs = {k: base64.b64decode(v) for k, v in (batch.get('inputs') or {}).items()}
    reqs = batch['requests']

    unknown = sorted({r['op'] for r in reqs} - OPS.keys())
    if unknown:
        print(f"pydriver: unknown op(s): {', '.join(unknown)}", file=sys.stderr)
        sys.exit(2)

    signal.signal(signal.SIGALRM, _alarm)
    with open(results_path, 'w', encoding='utf-8') as out:
        for req in reqs:
            kwargs = dict(req.get('args') or {})
            for key, value in (req.get('bytes_args') or {}).items():
                kwargs[key] = base64.b64decode(value)
            for key, name in (req.get('input_args') or {}).items():
                kwargs[key] = inputs[name]
            log = io.StringIO()
            result, err = {}, None
            signal.setitimer(signal.ITIMER_REAL, req.get('timeout') or DEFAULT_TIMEOUT_SECONDS)
            try:
                with contextlib.redirect_stdout(log):
                    result = OPS[req['op']](kwargs)
            except Exception as e:  # recorded for the Go side to compare
                err = f"{type(e).__name__}: {e}"
            finally:
                signal.setitimer(signal.ITIMER_REAL, 0)
            raw = result.get('out')
            out.write(json.dumps({
                'id': req['id'],
                'out': None if raw is None else base64.b64encode(bytes(raw)).decode('ascii'),
                'json': result.get('json'),
                'log': log.getvalue(),
                'err': err,
            }) + '\n')


if __name__ == '__main__':
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    main(sys.argv[1], sys.argv[2])
