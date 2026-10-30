#!/usr/bin/env python3
"""Exercise the real NeoProxy product on private port 18527 with cheap Codex.

An explicitly selected, read-only access lease is the only imported credential.
No refresh token is copied and no existing profile or production config is changed.
"""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import secrets
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request

from neopi_e2e import API, ProofError, capture_log, private_json, redact_json, safe_environment

ROOT = Path('/mnt/alias/m2/agents/scratch/neoproxy-product')
BASE = 'http://127.0.0.1:18527'
MODEL = 'gpt-6-luna'
SELECTOR = 'neoproxy/office-cheap'


def load(name):
    return json.loads((ROOT / name).read_text())


def session(role):
    access = load('access.json')
    api = API(BASE, access['operator_secret'])
    identity = access[role]
    api.call('POST', '/v8/session', {'handle': identity['handle'], 'password': identity['password']})
    return api


def prepare(args):
    if (ROOT / 'access.json').exists():
        raise ProofError('private lane already prepared; do not overwrite its credentials/configuration')
    ROOT.mkdir(parents=True, exist_ok=True, mode=0o700)
    source = Path(args.source_db).resolve(strict=True)
    now = int(time.time() * 1000)
    with sqlite3.connect('file:' + str(source) + '?mode=ro', uri=True) as store:
        row = store.execute(
            "SELECT json_extract(data,'$.access'),json_extract(data,'$.accountId'),"
            "json_extract(data,'$.expires') FROM auth_credentials WHERE id=? AND provider=? "
            "AND credential_type='oauth' AND disabled_cause IS NULL",
            (args.credential_id, 'openai-codex')).fetchone()
    if not row or not row[0] or not row[1] or row[2] <= now + 600000:
        raise ProofError('specific authorized Codex credential has no usable current access lease')
    # Deliberately select no refresh token: the canonical broker keeps its refresh authority.
    lease = {'type': 'codex', 'access_token': row[0], 'account_id': row[1],
             'expired': datetime.fromtimestamp(row[2] / 1000, timezone.utc).isoformat(),
             'label': 'Authorized product Codex access lease'}
    private_json(ROOT / 'codex-lease.json', lease)
    access = {'endpoint': BASE, 'virtual_selector': SELECTOR, 'concrete_model': 'codex/' + MODEL,
              'credential_source': {'kind': 'specific-read-only-access-lease',
                                    'credential_id': args.credential_id, 'expires_at_ms': row[2],
                                    'refresh_token_copied': False},
              'operator_secret': secrets.token_hex(32),
              'admin': {'handle': 'product-admin', 'password': secrets.token_hex(24)},
              'owner': {'handle': 'product-owner', 'password': secrets.token_hex(24)},
              'member': {'handle': 'product-member', 'password': secrets.token_hex(24)}}
    private_json(ROOT / 'access.json', access)
    private_json(ROOT / 'config.yml', {
        'config-version': 8, 'server': {'host': '127.0.0.1', 'port': 18527,
                                      'discovery': {'enabled': False}},
        'management': {'allow-remote': False, 'secret-key': access['operator_secret'],
                       'disable-control-panel': True, 'disable-auto-update-panel': True},
        'access': {'api-keys': [secrets.token_hex(32)]},
        'oauth': {'auth-dir': str(ROOT / 'auths')},
        'routing': {'retry': {'request-retry': 0, 'max-retry-interval': 0}},
        'observability': {'logs': {'debug': False, 'request-log': False,
                                   'logging-to-file': False}}})
    print(json.dumps({'prepared': True, 'manifest': str(ROOT / 'access.json'),
                      'endpoint': BASE, 'credential_count': 1,
                      'expires_at_ms': row[2], 'refresh_token_copied': False}))


def sensitive_values(access):
    values = [access['operator_secret']]
    values.extend(access[role]['password'] for role in ('admin', 'owner', 'member'))
    values.extend([load('codex-lease.json')['access_token']])
    if access.get('inference_key'):
        values.append(access['inference_key'])
    return values


def serve(args):
    access = load('access.json')
    home = ROOT / 'home'
    (home / 'tmp').mkdir(parents=True, exist_ok=True, mode=0o700)
    environment = safe_environment(home)
    environment['NEOPROXY_DATA_DIR'] = str(ROOT / 'data')
    command = [str(Path(args.binary).resolve(strict=True)), '--config', str(ROOT / 'config.yml'),
               '--local-model']
    with open(command[0], 'rb') as executable:
        digest = hashlib.file_digest(executable, 'sha256').hexdigest()
    private_json(ROOT / 'build.json', {'binary': command[0], 'sha256': digest,
                                      'source_sha': args.source_sha, 'command': command})
    process = subprocess.Popen(command, cwd=ROOT, env=environment,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    try:
        with (ROOT / 'server.log').open('ab') as output:
            capture_log(process.stdout, output, sensitive_values(access))
    finally:
        if process.poll() is None:
            process.terminate()
        process.wait()
    return process.returncode


def bootstrap(_args):
    access = load('access.json')
    if access.get('inference_key'):
        raise ProofError('bootstrap already completed; exercise the existing private product state instead')
    api = API(BASE, access['operator_secret'])
    identity = access['admin']
    try:
        admin = api.call('POST', '/v8/session/bootstrap', {
            'handle': identity['handle'], 'password': identity['password'], 'name': 'Product administrator'},
            operator=True)
    except ProofError as error:
        if 'HTTP 409' not in str(error):
            raise
        admin = api.call('POST', '/v8/session', {
            'handle': identity['handle'], 'password': identity['password']})
    access['admin']['id'] = admin['userId']
    private_json(ROOT / 'access.json', access)
    configured = api.call('GET', '/v8/management/config/oauth/auth-dir', operator=True)
    if configured != str(ROOT / 'auths'):
        raise ProofError('engine credential directory is not the explicitly isolated scratch directory')
    # v8 names this operational route credentials, not the legacy auth-files path.
    api.call('POST', '/v8/management/credentials?name=codex-product-lease.json',
             load('codex-lease.json'), operator=True)
    for role in ('owner', 'member'):
        user = api.call('POST', '/v8/neo/users', {
            'handle': access[role]['handle'], 'name': 'Product ' + role,
            'password': access[role]['password'], 'role': role, 'state': 'active',
            'providerAccess': [], 'grants': [], 'limits': {'rpm': None, 'tokensPerDay': None}})
        access[role]['id'] = user['id']
    vault = api.call('GET', '/v8/neo/vault')
    accounts = [account for account in vault['accounts'] if account['providerId'] == 'codex']
    if len(accounts) != 1:
        raise ProofError('isolated engine did not register exactly the one authorized Codex lease')
    account = accounts[0]
    account_id = account['id']
    api.call('PATCH', '/v8/neo/vault/accounts/' + account_id,
             {'ownerId': access['owner']['id']})
    api.call('PUT', '/v8/neo/vault/accounts/' + account_id + '/windows/day',
             {'capacityTokens': 1000000, 'startsAt': int(time.time() * 1000)})
    catalogue = api.call('GET', '/v8/neo/models')
    if not any(model.get('id') == 'codex/' + MODEL for model in catalogue):
        raise ProofError('real Codex account catalogue does not expose the authorized cheap luna model')
    virtual = api.call('POST', '/v8/neo/virtual-models', {
        'name': 'office-cheap', 'description': 'Cheap real Codex product lane',
        'stages': [{'id': 'cheap', 'model': 'codex/' + MODEL, 'accounts': [account_id],
                    'draw': 'alternate', 'perAccount': 1, 'cap': 1, 'window': 'day',
                    'per': 'user', 'effort': 'low'}],
        'terminal': {'kind': 'limit', 'message': 'Configured product-lane budget unavailable'}})
    user = api.call('GET', '/v8/neo/users/' + access['member']['id'])
    user['grants'] = [{'virtualModel': virtual['id'], 'allotment': 1}]
    api.call('PUT', '/v8/neo/users/' + user['id'], user)
    member = API(BASE, access['operator_secret'])
    member.call('POST', '/v8/session', {'handle': access['member']['handle'],
                                      'password': access['member']['password']})
    issued = member.call('POST', '/v8/neo/users/' + user['id'] + '/keys',
                         {'label': 'product-private-scoped', 'accountIds': [account_id]})
    access.update({'inference_key': issued['secret'], 'key_id': issued['key']['id'],
                   'account_ids': [account_id], 'virtual_model_id': virtual['id'],
                   'pool_cardinality': 1})
    private_json(ROOT / 'access.json', access)
    private_json(ROOT / 'setup-receipt.json', {
        'endpoint': BASE, 'account_ids': [account_id], 'virtual_selector': virtual['served'],
        'model': 'codex/' + MODEL, 'pool_cardinality': 1,
        'metered_window': {'kind': 'day', 'capacityTokens': 1000000,
                           'source': 'configured-metered-not-upstream-quota'},
        'credential_source': access['credential_source']})
    print(json.dumps({'ready': True, 'manifest': str(ROOT / 'access.json'),
                      'endpoint': BASE, 'selector': virtual['served'],
                      'concrete_model': 'codex/' + MODEL, 'pool_cardinality': 1}))


def inference(wire, streaming, key):
    prompt = 'What is 2 plus 3? Reply with only the single digit.'
    if wire == 'chat':
        path = '/v1/chat/completions'
        body = {'model': SELECTOR, 'messages': [{'role': 'user', 'content': prompt}],
                'max_completion_tokens': 128, 'stream': streaming}
        if streaming:
            body['stream_options'] = {'include_usage': True}
    elif wire == 'responses':
        path = '/v1/responses'
        body = {'model': SELECTOR, 'input': prompt, 'max_output_tokens': 128, 'stream': streaming}
    else:
        path = '/v1/messages'
        body = {'model': SELECTOR, 'messages': [{'role': 'user', 'content': prompt}],
                'max_tokens': 128, 'stream': streaming}
    request = urllib.request.Request(BASE + path, data=json.dumps(body).encode(),
                                     headers={'Authorization': 'Bearer ' + key,
                                              'Content-Type': 'application/json',
                                              'X-Client': 'neoproxy-product-' + wire,
                                              'anthropic-version': '2023-06-01'})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request) as response:
            raw = response.read()
            return {'status': response.status, 'request_id': response.headers.get('X-NeoProxy-Request-ID'),
                    'path': path, 'wire': wire, 'stream': streaming,
                    'body': raw.decode('utf-8')}
    except urllib.error.HTTPError as error:
        return {'status': error.code, 'request_id': error.headers.get('X-NeoProxy-Request-ID'),
                'path': path, 'wire': wire, 'stream': streaming,
                'body': error.read().decode('utf-8', errors='replace')}


def response_text(result):
    raw = result['body']
    if result['stream']:
        events = []
        for line in raw.splitlines():
            if line.startswith('data: ') and line[6:] != '[DONE]':
                events.append(json.loads(line[6:]))
        if result['wire'] == 'chat':
            return ''.join(choice.get('delta', {}).get('content') or ''
                           for event in events for choice in event.get('choices', []))
        if result['wire'] == 'responses':
            return ''.join(event.get('delta', '') for event in events
                           if event.get('type') == 'response.output_text.delta')
        return ''.join(event.get('delta', {}).get('text', '') for event in events
                       if event.get('type') == 'content_block_delta')
    body = json.loads(raw)
    if result['wire'] == 'chat':
        return ''.join(choice.get('message', {}).get('content') or '' for choice in body.get('choices', []))
    if result['wire'] == 'responses':
        return ''.join(block.get('text', '') for item in body.get('output', [])
                       for block in item.get('content', []) if block.get('type') == 'output_text')
    return ''.join(block.get('text', '') for block in body.get('content', []) if block.get('type') == 'text')


def exercise(args):
    if args.cycles < 2:
        raise ProofError('at least two cycles are required to prove repeated sessions and both streaming modes')
    if Path(args.receipt).name != args.receipt or not args.receipt.endswith('.json'):
        raise ProofError('receipt must be a JSON filename inside the private scratch directory')
    access = load('access.json')
    receipt = {'build': load('build.json'), 'setup': load('setup-receipt.json'),
               'status': 'not-passed', 'turns': [], 'identity_cycles': []}
    admin = session('admin')
    try:
        for cycle in range(args.cycles):
            member = session('member')
            who = member.call('GET', '/v8/session')
            if who['userId'] != access['member']['id']:
                raise ProofError('member login did not restore the expected current user')
            models = member.call('GET', '/v1/models', key=access['inference_key'])
            if [model['id'] for model in models['data']] != [SELECTOR]:
                raise ProofError('scoped user model discovery leaked or omitted its grant')
            member.call('DELETE', '/v8/session')
            try:
                member.call('GET', '/v8/session')
            except ProofError as error:
                if 'HTTP 401' not in str(error):
                    raise
            else:
                raise ProofError('logged-out server-side session remained valid')
            receipt['identity_cycles'].append({'cycle': cycle, 'login_user_id': who['userId'],
                                                'restored': True, 'logout_revoked': True,
                                                'discovered_selectors': [SELECTOR]})
            for wire in ('chat', 'responses', 'messages'):
                result = inference(wire, bool(cycle % 2), access['inference_key'])
                result['cycle'] = cycle
                receipt['turns'].append(result)
                if result['status'] != 200:
                    raise ProofError(f'{wire}: real upstream request returned HTTP {result["status"]}')
                result['text'] = response_text(result)
                record = admin.call('GET', '/v8/neo/requests/' + (result['request_id'] or ''))
                result['request'] = record
                decision = record['decision']
                tokens = record['tokens']
                if not result['text'].strip() or record['outcome'] != 'ok' or \
                        decision.get('model') != 'codex/' + MODEL or \
                        decision.get('accountId') not in access['account_ids'] or \
                        tokens.get('in', 0) <= 0 or tokens.get('out', 0) <= 0:
                    raise ProofError(f'{wire}: real response/account/usage evidence is incomplete')
        owner = session('owner')
        receipt['owner_stats'] = owner.call('GET', '/v8/neo/owner/stats')
        receipt['usage'] = admin.call('GET', '/v8/neo/usage')
        receipt['status'] = 'passed'
    except Exception as error:
        receipt['blocker'] = str(error) if isinstance(error, ProofError) else type(error).__name__
        raise
    finally:
        private_json(ROOT / args.receipt, redact_json(receipt, sensitive_values(access)))
    print(json.dumps({'status': receipt['status'], 'real_turns': len(receipt['turns']),
                      'identity_cycles': len(receipt['identity_cycles']),
                      'receipt': str(ROOT / args.receipt)}))


def authority(_args):
    """Exercise current authority against real SQLite/HTTP state without paid calls."""
    access = load('access.json')
    admin, member, owner = session('admin'), session('member'), session('owner')
    receipt = {'status': 'not-passed', 'checks': [], 'build': load('build.json')}
    sensitive = sensitive_values(access)
    original_owner = admin.call('GET', '/v8/neo/users/' + access['owner']['id'])
    probe_user = None

    def denied(name, api, method, path, body=None, key=None, status=403):
        try:
            api.call(method, path, body, key=key)
        except ProofError as error:
            if f'HTTP {status}' not in str(error):
                raise
            receipt['checks'].append({'name': name, 'status': status, 'denied': True})
        else:
            raise ProofError(name + ': unauthorized request was accepted')

    try:
        denied('member cannot administer users', member, 'GET', '/v8/neo/users')
        denied('member cannot read another user', member, 'GET',
               '/v8/neo/users/' + access['owner']['id'])
        denied('owner is not organization administrator', owner, 'GET', '/v8/neo/users')
        denied('owner cannot mint an unscoped pool key', owner, 'POST',
               '/v8/neo/users/' + access['owner']['id'] + '/keys', {'label': 'must-reject'})
        denied('owner cannot mint a foreign or nonexistent pool scope', owner, 'POST',
               '/v8/neo/users/' + access['owner']['id'] + '/keys',
               {'label': 'must-reject', 'accountIds': ['acct-not-owned']})
        denied('member cannot bypass virtual grant through direct model', member, 'POST',
               '/v1/chat/completions',
               {'model': 'neoproxy/codex/' + MODEL, 'messages': [
                   {'role': 'user', 'content': 'Reply with one digit.'}], 'max_completion_tokens': 8},
               key=access['inference_key'])
        owned = dict(original_owner)
        owned['grants'] = [{'virtualModel': access['virtual_model_id'], 'allotment': 1}]
        admin.call('PUT', '/v8/neo/users/' + owned['id'], owned)
        owner_key = owner.call('POST', '/v8/neo/users/' + owned['id'] + '/keys',
                               {'label': 'temporary-owner-scope', 'accountIds': access['account_ids']})
        sensitive.append(owner_key['secret'])
        discovered = owner.call('GET', '/v1/models', key=owner_key['secret'])
        if [model['id'] for model in discovered['data']] != [SELECTOR]:
            raise ProofError('valid owner pool key did not discover its current grant')
        receipt['checks'].append({'name': 'owner pool-scoped key follows current grant',
                                   'account_ids': access['account_ids'], 'selectors': [SELECTOR]})
        owned['grants'] = []
        admin.call('PUT', '/v8/neo/users/' + owned['id'], owned)
        if owner.call('GET', '/v1/models', key=owner_key['secret'])['data']:
            raise ProofError('removed current grant remained visible to an existing key')
        receipt['checks'].append({'name': 'grant removal immediately narrows existing key', 'selectors': []})
        owner.call('DELETE', '/v8/neo/users/' + owned['id'] + '/keys/' + owner_key['key']['id'])
        denied('revoked owner pool key immediately rejected', owner, 'GET', '/v1/models',
               key=owner_key['secret'], status=401)
        password = secrets.token_hex(20)
        sensitive.append(password)
        probe_user = admin.call('POST', '/v8/neo/users', {
            'handle': 'product-probe-' + secrets.token_hex(4), 'name': 'Isolated authority probe',
            'password': password, 'role': 'member', 'state': 'active', 'providerAccess': [],
            'grants': [{'virtualModel': access['virtual_model_id'], 'allotment': 1}],
            'limits': {'rpm': None, 'tokensPerDay': None}})
        probe = API(BASE, access['operator_secret'])
        login = {'handle': probe_user['handle'], 'password': password}
        probe.call('POST', '/v8/session', login)
        issued = probe.call('POST', '/v8/neo/users/' + probe_user['id'] + '/keys',
                            {'label': 'authority-probe', 'accountIds': access['account_ids']})
        sensitive.append(issued['secret'])
        probe_user['state'] = 'suspended'
        admin.call('PUT', '/v8/neo/users/' + probe_user['id'], probe_user)
        denied('suspension invalidates existing session', probe, 'GET', '/v8/session', status=401)
        denied('suspension rejects retained inference key', probe, 'GET', '/v1/models',
               key=issued['secret'])
        probe_user['state'] = 'active'
        admin.call('PUT', '/v8/neo/users/' + probe_user['id'], probe_user)
        if not probe.call('GET', '/v1/models', key=issued['secret'])['data']:
            raise ProofError('reactivation did not restore the retained key current grant')
        receipt['checks'].append({'name': 'reactivation restores current scoped key grant', 'active': True})
        probe.call('POST', '/v8/session', login)
        new_password = secrets.token_hex(20)
        sensitive.append(new_password)
        probe.call('POST', '/v8/neo/users/' + probe_user['id'] + '/password',
                   {'currentPassword': password, 'password': new_password})
        denied('password change invalidates old session', probe, 'GET', '/v8/session', status=401)
        denied('password change invalidates old inference key', probe, 'GET', '/v1/models',
               key=issued['secret'], status=401)
        probe.call('POST', '/v8/session', {'handle': probe_user['handle'], 'password': new_password})
        if probe.call('GET', '/v8/session')['userId'] != probe_user['id']:
            raise ProofError('new password did not support fresh session restoration')
        receipt['checks'].append({'name': 'changed password supports fresh login', 'restored': True})
        receipt['status'] = 'passed'
    except Exception as error:
        receipt['blocker'] = str(error) if isinstance(error, ProofError) else type(error).__name__
        raise
    finally:
        admin.call('PUT', '/v8/neo/users/' + original_owner['id'], original_owner)
        if probe_user is not None:
            probe_user['state'] = 'suspended'
            admin.call('PUT', '/v8/neo/users/' + probe_user['id'], probe_user)
        private_json(ROOT / 'authority-receipt.json', redact_json(receipt, sensitive))
    print(json.dumps({'status': receipt['status'], 'authority_checks': len(receipt['checks']),
                      'receipt': str(ROOT / 'authority-receipt.json')}))


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='action', required=True)
    preparation = commands.add_parser('prepare')
    preparation.add_argument('--source-db', default=str(Path.home() / '.omp/agent/agent.db'))
    preparation.add_argument('--credential-id', type=int, required=True)
    preparation.set_defaults(func=prepare)
    server = commands.add_parser('serve')
    server.add_argument('--binary', required=True)
    server.add_argument('--source-sha', required=True)
    server.set_defaults(func=serve)
    commands.add_parser('bootstrap').set_defaults(func=bootstrap)
    commands.add_parser('authority').set_defaults(func=authority)
    run = commands.add_parser('exercise')
    run.add_argument('--cycles', type=int, default=2)
    run.add_argument('--receipt', default='product-receipt.json')
    run.set_defaults(func=exercise)
    args = parser.parse_args()
    try:
        result = args.func(args)
    except (ProofError, OSError, ValueError, sqlite3.Error) as error:
        print(json.dumps({'status': 'not-passed', 'blocker': str(error)
                          if isinstance(error, ProofError) else type(error).__name__}), file=sys.stderr)
        return 1
    return result or 0


if __name__ == '__main__':
    sys.exit(main())
