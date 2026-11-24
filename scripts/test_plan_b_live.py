#!/usr/bin/env python3
"""Opt-in acceptance for an already enabled, independent rdev Plan B.

Uses only generated temporary files. --native-cli uses an isolated temporary
Claude home and a scripted local Messages API, never a real model/account.
"""
import argparse
import atexit
import base64
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import textwrap

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--state-dir', type=Path, default=Path.home() / '.rdev')
parser.add_argument('--native-cli', action='store_true')
options = parser.parse_args()
profile = json.loads((options.state_dir / 'plan-b/profile.json').read_text())
conf = '/data/devices/' + profile['device_id'] + '/ssh_config'
ssh = ['ssh', '-T', '-o', 'StrictHostKeyChecking=yes', '-o', 'ForwardAgent=no', '-o', 'BatchMode=yes']
if profile.get('ssh_config'):
    ssh += ['-F', profile['ssh_config']]
if os.name != 'nt':
    mux_dir = tempfile.TemporaryDirectory(prefix='rdev-b-ssh-')
    mux_path = str(Path(mux_dir.name) / 'mux')
    if len(mux_path) < 95:
        ssh += ['-o', 'ControlMaster=auto', '-o', 'ControlPath='+mux_path, '-o', 'ControlPersist=30']
        atexit.register(mux_dir.cleanup)
        atexit.register(lambda: subprocess.run(ssh[:-1] + ['-O','exit',profile['ssh']], capture_output=True, timeout=5))
ssh += [profile['ssh']]
checks = []

def remote(arguments, data=None, timeout=45):
    return subprocess.run(ssh + [shlex.join(['docker', 'exec', '-i', profile['container']] + arguments)],
                          input=data, capture_output=True, timeout=timeout)

def good(name, condition, details=''):
    if not condition:
        raise AssertionError(name + ': ' + str(details))
    checks.append(name)
    print('PASS ' + name, flush=True)

with tempfile.TemporaryDirectory(prefix='rdev-b-accept-') as directory:
    root = Path(directory)
    line = "cat; printf ' ERR中文' >&2; exit 7"
    result = remote(['ssh', '-F', conf, 'device', line], b'INPUT\x00\xff')
    good('stdin/stdout/stderr/exit over actual VPS-Mac link',
         result.returncode == 7 and result.stdout == b'INPUT\x00\xff' and result.stderr == ' ERR中文'.encode(), result.stderr)
    def one(index):
        r = remote(['ssh', '-F', conf, 'device', 'printf %s ' + str(index)])
        return r.returncode == 0 and r.stdout == str(index).encode()
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        good('four concurrent native commands', all(pool.map(one, range(4))))
    data = os.urandom(2 * 1024 * 1024)
    original = root / '二进制 file.bin'
    original.write_bytes(data)
    staged = '/data/workspace/rdev-accept-' + profile['device_id'] + '.bin'
    result = remote(['scp', '-q', '-F', conf, 'device:' + str(original), staged], timeout=180)
    good('Mac to VPS SFTP', result.returncode == 0, result.stderr)
    result = remote(['sha256sum', staged])
    good('binary SHA256 integrity', result.stdout.decode().split()[0] == hashlib.sha256(data).hexdigest())
    back = root / '回传.bin'
    result = remote(['scp', '-q', '-F', conf, staged, 'device:' + str(back)], timeout=180)
    good('VPS to Mac SFTP', result.returncode == 0 and back.read_bytes() == data, result.stderr)
    remote(['rm', '-f', staged])
    if options.native_cli:
        marker = root / 'native-harness.txt'
        command = 'ssh -F ' + shlex.quote(conf) + ' device ' + shlex.quote(
            'printf rdev-native-fixture > ' + shlex.quote(str(marker)) + '; cat ' + shlex.quote(str(marker)))
        encoded = base64.b64encode(json.dumps({'command': command}).encode()).decode()
        fixture = textwrap.dedent(r'''
            import base64, json, os, pathlib, subprocess, tempfile, threading
            from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
            command = json.loads(base64.b64decode('__METADATA__'))['command']
            seen = []
            class Model(BaseHTTPRequestHandler):
                def log_message(self, *args): pass
                def do_HEAD(self): self.send_response(200); self.end_headers()
                def do_POST(self):
                    request = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))))
                    request_path = self.path.split('?',1)[0]
                    if request_path.endswith('/count_tokens'):
                        self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers()
                        self.wfile.write(b'{"input_tokens":100}'); return
                    if not request_path.endswith('/messages'):
                        self.send_response(404); self.end_headers(); return
                    user = request.get('messages', [])[-1:]
                    results = [v for m in user if m.get('role') == 'user' and isinstance(m.get('content'), list)
                               for v in m['content'] if v.get('type') == 'tool_result']
                    if request.get('tools') and not any(m.get('role') == 'assistant' for m in request.get('messages', [])):
                        content = [{'type':'tool_use','id':'toolu_rdevfixture','name':'Bash',
                                    'input':{'command':command,'description':'Verify the owned temporary rdev fixture'}}]
                        stop = 'tool_use'
                    else:
                        seen.extend(results)
                        content = [{'type':'text','text':'RDEV_NATIVE_FIXTURE_COMPLETE'}]
                        stop = 'end_turn'
                    usage = {'input_tokens':100,'output_tokens':10,'cache_creation_input_tokens':0,'cache_read_input_tokens':0}
                    message = {'id':'msg_rdevfixture','type':'message','role':'assistant','model':request.get('model','scripted'),
                               'content':[],'stop_reason':None,'stop_sequence':None,'usage':usage}
                    if not request.get('stream'):
                        self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers()
                        self.wfile.write(json.dumps({**message,'content':content,'stop_reason':stop}).encode()); return
                    self.send_response(200); self.send_header('Content-Type','text/event-stream'); self.end_headers()
                    def emit(kind, **values):
                        self.wfile.write(('event: '+kind+'\ndata: '+json.dumps({'type':kind,**values})+'\n\n').encode()); self.wfile.flush()
                    emit('message_start', message=message)
                    for i, block in enumerate(content):
                        first = {**block,'input':{}} if block['type']=='tool_use' else {'type':'text','text':''}
                        emit('content_block_start',index=i,content_block=first)
                        delta = {'type':'input_json_delta','partial_json':json.dumps(block['input'])} if block['type']=='tool_use' else {'type':'text_delta','text':block['text']}
                        emit('content_block_delta',index=i,delta=delta); emit('content_block_stop',index=i)
                    emit('message_delta',delta={'stop_reason':stop,'stop_sequence':None},usage={'output_tokens':10})
                    emit('message_stop')
            server = ThreadingHTTPServer(('127.0.0.1',0), Model)
            threading.Thread(target=server.serve_forever,daemon=True).start()
            with tempfile.TemporaryDirectory(prefix='rdev-native-model-') as home:
                env = {**os.environ,'HOME':home,'CLAUDE_CONFIG_DIR':home+'/.claude',
                       'ANTHROPIC_BASE_URL':'http://127.0.0.1:'+str(server.server_port),
                       'ANTHROPIC_API_KEY':'rdev-scripted-fixture','ANTHROPIC_AUTH_TOKEN':'',
                       'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC':'1','DISABLE_TELEMETRY':'1',
                       'DISABLE_ERROR_REPORTING':'1','DISABLE_AUTOUPDATER':'1'}
                proc = subprocess.run(['claude','--print','--verbose','--output-format','stream-json',
                                       '--allowedTools','Bash','--model','claude-sonnet-4-6',
                                       'Run the single temporary verification command returned by the local scripted model.'],
                                      cwd=home, env=env, capture_output=True, text=True, timeout=90)
                success = proc.returncode == 0 and 'RDEV_NATIVE_FIXTURE_COMPLETE' in proc.stdout and bool(seen)
                print(json.dumps({'native_cli':success,'exit':proc.returncode,'tool_results':len(seen),
                                  'diagnostic':proc.stderr[-1500:] if not success else ''}),flush=True)
                if not success: print(proc.stdout[-3000:],flush=True)
                server.shutdown()
                raise SystemExit(0 if success else 1)
        ''').replace('__METADATA__', encoded)
        result = remote(['python3', '-'], fixture.encode(), timeout=120)
        good('official remote Claude CLI / scripted API / local tool / result loop',
             result.returncode == 0 and marker.exists() and marker.read_text() == 'rdev-native-fixture',
             result.stdout.decode(errors='replace') + result.stderr.decode(errors='replace'))
print(json.dumps({'checks': len(checks), 'model': 'scripted only' if options.native_cli else 'not invoked', 'status': 'passed'}))
