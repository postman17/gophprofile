#!/usr/bin/env python3
"""Exercise the local Compose stack end to end. Uses only Python's standard library."""
import json
import struct
import time
import urllib.error
import urllib.parse
import urllib.request
import zlib


def request(url, data=None, headers=None, method=None):
    with urllib.request.urlopen(urllib.request.Request(url, data=data, headers=headers or {}, method=method), timeout=10) as response:
        return response.read(), response.headers


def get_json(url):
    return json.loads(request(url)[0])


def eventually(check, timeout=90):
    deadline = time.monotonic() + timeout
    while True:
        result = check()
        if result:
            return result
        if time.monotonic() >= deadline:
            raise AssertionError(f"Timed out: {check.__name__}")
        time.sleep(2)


def png():
    def chunk(kind, data):
        return struct.pack('!I', len(data)) + kind + data + struct.pack('!I', zlib.crc32(kind + data))
    return (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('!2I5B', 32, 32, 8, 2, 0, 0, 0))
            + chunk(b'IDAT', zlib.compress((b'\0' + b'\x44\x88\xcc' * 32) * 32)) + chunk(b'IEND', b''))


def main():
    assert get_json('http://localhost:3200/health')['status'] == 'ok'
    boundary = 'gophprofile-observability-smoke'
    body = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="smoke.png"\r\n'
            'Content-Type: image/png\r\n\r\n').encode() + png() + f'\r\n--{boundary}--\r\n'.encode()
    response, headers = request('http://localhost:3200/api/v1/avatars', body, {
        'Content-Type': f'multipart/form-data; boundary={boundary}', 'X-User-ID': 'observability-smoke'})
    avatar = json.loads(response)
    trace_id = headers['X-Trace-ID']
    avatar_url = 'http://localhost:3200/api/v1/avatars/' + avatar['id']
    print('Uploaded avatar:', avatar['id'], 'trace:', trace_id)
    try:
        def processed():
            meta = get_json(avatar_url + '/metadata')
            return meta if meta['processing_status'] == 'completed' else None
        metadata = eventually(processed)
        assert len(metadata['thumbnails']) == 2
        for thumbnail in metadata['thumbnails']:
            content, _ = request('http://localhost:3200' + thumbnail['url'])
            assert len(content) > 0

        def distributed_trace():
            try:
                traces = get_json('http://localhost:16686/api/traces/' + trace_id)['data']
            except urllib.error.HTTPError as error:
                if error.code == 404:  # Trace export is asynchronous.
                    return None
                raise
            if not traces:
                return None
            services = {p['serviceName'] for p in traces[0]['processes'].values()}
            return traces[0] if {'gophprofile-server', 'gophprofile-worker'} <= services else None
        trace_data = eventually(distributed_trace)
        operations = {span['operationName'] for span in trace_data['spans']}
        assert 'avatar.upload' in operations
        assert any(op.startswith('postgresql ') for op in operations)
        assert 's3.Put' in operations
        assert any(op.startswith('kafka publish ') for op in operations)
        assert any(op.startswith('kafka process ') for op in operations)
        print('Distributed trace verified:', len(trace_data['spans']), 'spans across API and worker')

        def correlated_logs():
            query = '{service=~"server|worker"} | json | trace_id="' + trace_id + '"'
            result = get_json('http://localhost:3100/loki/api/v1/query_range?' + urllib.parse.urlencode({'query': query, 'limit': 100}))
            streams = result['data']['result']
            return streams if {'server', 'worker'} <= {s['stream']['service'] for s in streams} else None
        logs = eventually(correlated_logs)
        print('Correlated Loki logs verified:', sum(len(s['values']) for s in logs), 'records')

        def metrics_available():
            query = 'avatars_uploads_total{job="gophprofile-server",status="success"}'
            result = get_json('http://localhost:9090/api/v1/query?' + urllib.parse.urlencode({'query': query}))
            return result['data']['result']
        assert float(eventually(metrics_available)[0]['value'][1]) >= 1
        targets = get_json('http://localhost:9090/api/v1/targets')['data']['activeTargets']
        assert all(t['health'] == 'up' for t in targets), targets
        print('All Prometheus scrape targets are healthy')
    finally:
        request(avatar_url, headers={'X-User-ID': 'observability-smoke'}, method='DELETE')
        print('Smoke avatar scheduled for deletion')


if __name__ == '__main__':
    main()
