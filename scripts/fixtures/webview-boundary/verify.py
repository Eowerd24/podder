import json
import re
import sys
from collections import Counter
from pathlib import Path

expected_calls = {'trustedjson': 1, 'trustedchunk': 1}
expected_codes = {'200': 3, '415': 1, '405': 4, '403': 6}
expected_scenarios = {'trusted': 1, 'remote/top': 1, 'remote/frame-after': 1, 'remote/sandbox-after': 1}
for name in sys.argv[1:]:
    lines = Path(name).read_text().splitlines()
    summaries = [json.loads(line.removeprefix('FIXTURE_RESULT ')) for line in lines if line.startswith('FIXTURE_RESULT ')]
    assert len(summaries) == 1, (name, summaries)
    result = summaries[0]
    assert result['passed'] is True and not result.get('failure'), (name, result)
    assert result['calls'] == expected_calls, (name, result)
    assert result['codes'] == expected_codes, (name, result)
    assert result['scenarios'] == expected_scenarios, (name, result)
    assert result['bootstrap'] == {'top': 1, 'child': 1}, (name, result)
    assert result['forbidden'] == 2 and result['forbiddenLoads'] == 0 and result['remoteTop'] == 1, (name, result)
    calls = Counter(json.loads(line[5:]) for line in lines if line.startswith('PING '))
    assert calls == expected_calls, (name, calls)
    reports = [json.loads(line[7:].split(' nativeOrigin=')[0]) for line in lines if line.startswith('REPORT ')]
    bootstrap = [item for item in reports if 'bootstrap' in item]
    assert Counter((item['bootstrap'], item['top']) for item in bootstrap) == {(True, True): 1, (False, False): 1}, (name, bootstrap)
    violations = [item for item in reports if item.get('directive') == 'frame-src']
    assert len(violations) == 2, (name, violations)
    statuses = Counter(re.search(r' status=(\d+)', line)[1] for line in lines if line.startswith('RESULT ') and '/wails/runtime' in line)
    assert statuses == expected_codes, (name, statuses)
    assert 'REMOTE_REQ GET /frame' not in lines and 'REMOTE_REQ GET /sandbox' not in lines, name
    assert lines.count('REMOTE_REQ GET /top') == 1, name
    print(json.dumps({'log': name, **result}))
