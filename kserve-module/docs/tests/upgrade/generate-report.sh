#!/bin/bash
# Generate a single-file HTML report from upgrade-diff snapshots + load test logs.
#
# Usage:
#   ./generate-report.sh \
#     --diff <diff-report.txt> \
#     --snapshots <snapshots-dir> \
#     --before <snapshot-name> \
#     --after <snapshot-name> \
#     --load-dir <dir-with-jsonl-files> \
#     --output <output.html>

set -uo pipefail

DIFF_FILE=""
SNAPSHOTS_DIR=""
BEFORE=""
AFTER=""
LOAD_DIR=""
OUTPUT=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --diff) DIFF_FILE="$2"; shift 2 ;;
    --snapshots) SNAPSHOTS_DIR="$2"; shift 2 ;;
    --before) BEFORE="$2"; shift 2 ;;
    --after) AFTER="$2"; shift 2 ;;
    --load-dir) LOAD_DIR="$2"; shift 2 ;;
    --output) OUTPUT="$2"; shift 2 ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

[[ -z "$OUTPUT" ]] && { echo "Error: --output is required"; exit 1; }

python3 - "$DIFF_FILE" "$SNAPSHOTS_DIR" "$BEFORE" "$AFTER" "$LOAD_DIR" "$OUTPUT" <<'PYTHON_SCRIPT'
import json, sys, os, html, glob
from collections import defaultdict
from datetime import datetime

diff_file, snapshots_dir, before_name, after_name, load_dir, output_file = sys.argv[1:7]

# --- Load test stats ---

def collect_load_stats(logfile):
    total = 0; ok_count = 0; latencies = []
    first_ts = ''; last_ts = ''
    buckets = defaultdict(lambda: {'ok': 0, 'fail': 0})
    max_down_sec = 0; fail_streak_start = None

    with open(logfile) as f:
        for line in f:
            line = line.strip()
            if not line: continue
            try: entry = json.loads(line)
            except: continue

            total += 1
            ts = entry.get('ts', '')
            is_ok = entry.get('ok', False)
            lat = entry.get('latency_ms', 0)
            if not first_ts: first_ts = ts
            last_ts = ts
            ts_sec = ts[:19]

            if is_ok:
                ok_count += 1
                latencies.append(lat)
                buckets[ts_sec]['ok'] += 1
                if fail_streak_start:
                    try:
                        d1 = datetime.fromisoformat(fail_streak_start.replace('Z','+00:00'))
                        d2 = datetime.fromisoformat(ts.replace('Z','+00:00'))
                        max_down_sec = max(max_down_sec, (d2 - d1).total_seconds())
                    except: pass
                    fail_streak_start = None
            else:
                buckets[ts_sec]['fail'] += 1
                if not fail_streak_start:
                    fail_streak_start = ts

    if total == 0:
        return {'total':0,'ok':0,'fail':0,'fail_pct':'0','p50':0,'p95':0,'p99':0,
                'max_downtime_s':0,'first_ts':'','last_ts':'','timeline':[]}

    fail = total - ok_count
    latencies.sort()
    return {
        'total': total, 'ok': ok_count, 'fail': fail,
        'fail_pct': f'{(fail/total*100):.1f}',
        'p50': latencies[len(latencies)//2] if latencies else 0,
        'p95': latencies[int(len(latencies)*0.95)] if latencies else 0,
        'p99': latencies[int(len(latencies)*0.99)] if latencies else 0,
        'max_downtime_s': round(max_down_sec, 1),
        'first_ts': first_ts, 'last_ts': last_ts,
        'timeline': [{'ts': k, 'ok': v['ok'], 'fail': v['fail']}
                      for k, v in sorted(buckets.items())],
    }

# Collect all load test files
load_stats = {}
for logfile in sorted(glob.glob(os.path.join(load_dir, 'load-test-*.jsonl'))):
    label = os.path.basename(logfile).replace('load-test-','').replace('.jsonl','')
    load_stats[label] = collect_load_stats(logfile)

# --- Pod changes ---

def collect_pod_changes():
    bp = os.path.join(snapshots_dir, before_name, 'pods.json')
    ap = os.path.join(snapshots_dir, after_name, 'pods.json')
    if not os.path.isfile(bp) or not os.path.isfile(ap):
        return {'restarted':[],'new':[],'gone':[],'image_changed':[]}
    with open(bp) as f: before = json.load(f)
    with open(ap) as f: after = json.load(f)
    bmap = {(p['namespace'],p['name']): p for p in before}
    amap = {(p['namespace'],p['name']): p for p in after}
    restarted = []; image_changed = []
    for k, ap in amap.items():
        bp = bmap.get(k)
        if not bp: continue
        br = sum(bp.get('restartCounts',[])); ar = sum(ap.get('restartCounts',[]))
        if ar > br: restarted.append({'name':f'{k[0]}/{k[1]}','before':br,'after':ar})
        if ap.get('images') != bp.get('images'):
            image_changed.append({'name':f'{k[0]}/{k[1]}','before':bp.get('images',[]),'after':ap.get('images',[])})
    new_pods = [{'name':f'{k[0]}/{k[1]}'} for k in amap if k not in bmap]
    gone_pods = [{'name':f'{k[0]}/{k[1]}'} for k in bmap if k not in amap]
    return {'restarted':restarted,'new':new_pods,'gone':gone_pods,'image_changed':image_changed}

pod_changes = collect_pod_changes()

# --- Resource diff summary ---

def collect_resource_summary():
    result = {}
    bd = os.path.join(snapshots_dir, before_name)
    ad = os.path.join(snapshots_dir, after_name)
    for kind in ['deployments','services','configmaps']:
        bp = os.path.join(bd, kind); ap = os.path.join(ad, kind)
        bf = set(os.listdir(bp)) if os.path.isdir(bp) else set()
        af = set(os.listdir(ap)) if os.path.isdir(ap) else set()
        modified = 0
        for f in bf & af:
            with open(os.path.join(bp,f)) as a, open(os.path.join(ap,f)) as b:
                if a.read() != b.read(): modified += 1
        result[kind] = {'added':len(af-bf),'removed':len(bf-af),'modified':modified}
    return result

resource_summary = collect_resource_summary()

# --- CR status ---

def collect_cr_status():
    try: import yaml
    except: return {}
    result = {}
    for subdir in ['crs','serving']:
        d = os.path.join(snapshots_dir, after_name, subdir)
        if not os.path.isdir(d): continue
        for fname in os.listdir(d):
            try:
                with open(os.path.join(d, fname)) as f:
                    for doc in yaml.safe_load_all(f):
                        if not doc: continue
                        items = doc.get('items',[doc]) if 'items' not in doc else doc['items']
                        for item in items:
                            if not item or not item.get('kind'): continue
                            name = item.get('metadata',{}).get('name','')
                            kind = item.get('kind','')
                            status = item.get('status',{})
                            conds = status.get('conditions',[])
                            ready = next((c for c in conds if c.get('type')=='Ready'), None)
                            result[f'{kind}/{name}'] = {
                                'phase': status.get('phase',''),
                                'ready': ready.get('status','') if ready else '',
                            }
            except: pass
    return result

cr_status = collect_cr_status()

# --- Diff text ---

diff_text = ''
if diff_file and os.path.isfile(diff_file):
    with open(diff_file) as f:
        diff_text = html.escape(f.read())

# --- Timestamps ---

def read_ts(name):
    p = os.path.join(snapshots_dir, name, 'metadata.txt')
    if os.path.isfile(p):
        with open(p) as f:
            return f.readline().strip().replace('Timestamp: ','')
    return 'unknown'

before_ts = read_ts(before_name)
after_ts = read_ts(after_name)

# --- Build HTML ---

total_reqs = sum(s['total'] for s in load_stats.values())
total_fail = sum(s['fail'] for s in load_stats.values())
max_downtime = max((s['max_downtime_s'] for s in load_stats.values()), default=0)
overall_pass = total_fail == 0

pass_badge = '<span class="badge pass">PASS</span>' if overall_pass else '<span class="badge fail">FAIL</span>'

def make_timeline_svg(timeline, width=800, height=30):
    if not timeline: return '<p>No data</p>'
    n = len(timeline)
    bar_w = max(1, width / n)
    rects = []
    for i, t in enumerate(timeline):
        total_in_sec = t['ok'] + t['fail']
        fail_ratio = t['fail'] / total_in_sec if total_in_sec > 0 else 0
        if fail_ratio == 0: color = '#22c55e'
        elif fail_ratio < 0.5: color = '#f59e0b'
        else: color = '#ef4444'
        rects.append(f'<rect x="{i*bar_w:.1f}" y="0" width="{bar_w+0.5:.1f}" height="{height}" fill="{color}"><title>{t["ts"]} ok:{t["ok"]} fail:{t["fail"]}</title></rect>')
    return f'<svg width="{width}" height="{height}" style="border:1px solid #333;border-radius:4px">{"".join(rects)}</svg>'

# Load test sections
load_html = ''
for label, s in load_stats.items():
    if s['total'] == 0: continue
    svg = make_timeline_svg(s['timeline'])
    load_html += f'''
    <div class="card">
      <h3>{html.escape(label)}</h3>
      <div class="stats-grid">
        <div><span class="stat-label">Total</span><span class="stat-value">{s['total']:,}</span></div>
        <div><span class="stat-label">OK</span><span class="stat-value ok">{s['ok']:,}</span></div>
        <div><span class="stat-label">Failed</span><span class="stat-value {'fail' if s['fail']>0 else ''}">{s['fail']:,} ({s['fail_pct']}%)</span></div>
        <div><span class="stat-label">Max Downtime</span><span class="stat-value {'fail' if s['max_downtime_s']>0 else ''}">{s['max_downtime_s']}s</span></div>
        <div><span class="stat-label">p50</span><span class="stat-value">{s['p50']}ms</span></div>
        <div><span class="stat-label">p95</span><span class="stat-value">{s['p95']}ms</span></div>
        <div><span class="stat-label">p99</span><span class="stat-value">{s['p99']}ms</span></div>
      </div>
      <h4>Availability Timeline</h4>
      <div class="timeline">{svg}</div>
      <p class="timeline-label"><span class="dot ok-dot"></span> OK <span class="dot warn-dot"></span> Partial <span class="dot fail-dot"></span> Failed &nbsp; {s['first_ts']} ~ {s['last_ts']}</p>
    </div>'''

# Pod changes
pod_html = '<div class="card"><h3>Pod Changes</h3>'
if pod_changes.get('restarted'):
    pod_html += '<h4>Restarted</h4><ul>'
    for p in pod_changes['restarted']:
        pod_html += f'<li>{html.escape(p["name"])}: {p["before"]} &rarr; {p["after"]} restarts</li>'
    pod_html += '</ul>'
if pod_changes.get('new'):
    pod_html += '<h4>New Pods</h4><ul>'
    for p in pod_changes['new']:
        pod_html += f'<li class="added">+ {html.escape(p["name"])}</li>'
    pod_html += '</ul>'
if pod_changes.get('gone'):
    pod_html += '<h4>Gone Pods</h4><ul>'
    for p in pod_changes['gone']:
        pod_html += f'<li class="removed">- {html.escape(p["name"])}</li>'
    pod_html += '</ul>'
if pod_changes.get('image_changed'):
    pod_html += '<h4>Image Changed</h4><ul>'
    for p in pod_changes['image_changed']:
        pod_html += f'<li>{html.escape(p["name"])}<br><code>{p["before"]}</code> &rarr; <code>{p["after"]}</code></li>'
    pod_html += '</ul>'
if not any(pod_changes.get(k) for k in ['restarted','new','gone','image_changed']):
    pod_html += '<p>No pod changes detected</p>'
pod_html += '</div>'

# Resource summary
res_html = '<div class="card"><h3>Resource Diff Summary</h3><table><tr><th>Type</th><th>Added</th><th>Removed</th><th>Modified</th></tr>'
for kind in ['deployments','services','configmaps']:
    r = resource_summary.get(kind, {})
    res_html += f'<tr><td>{kind}</td><td class="added">{r.get("added",0)}</td><td class="removed">{r.get("removed",0)}</td><td>{r.get("modified",0)}</td></tr>'
res_html += '</table></div>'

# CR status
cr_html = '<div class="card"><h3>CR Status (post-upgrade)</h3><table><tr><th>CR</th><th>Phase</th><th>Ready</th></tr>'
for name, s in cr_status.items():
    rc = 'ok' if s.get('ready') == 'True' else 'fail'
    cr_html += f'<tr><td>{html.escape(name)}</td><td>{html.escape(s.get("phase",""))}</td><td class="{rc}">{html.escape(s.get("ready",""))}</td></tr>'
cr_html += '</table></div>'

# Raw diff
diff_html = f'<div class="card"><details><summary><h3 style="display:inline">Raw Diff Output</h3></summary><pre>{diff_text}</pre></details></div>'

with open(output_file, 'w') as out:
    out.write(f'''<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>KServe Module Upgrade Test Report</title>
<style>
  * {{ margin: 0; padding: 0; box-sizing: border-box; }}
  body {{ font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, monospace; background: #0f0f0f; color: #e0e0e0; padding: 24px; line-height: 1.6; }}
  h1 {{ font-size: 1.5rem; margin-bottom: 8px; }}
  h2 {{ font-size: 1.1rem; color: #999; margin-bottom: 24px; font-weight: normal; }}
  h3 {{ font-size: 1rem; margin-bottom: 12px; color: #ccc; }}
  h4 {{ font-size: 0.85rem; margin: 12px 0 6px; color: #aaa; }}
  .card {{ background: #1a1a1a; border: 1px solid #333; border-radius: 8px; padding: 20px; margin-bottom: 16px; }}
  .summary {{ display: flex; gap: 24px; align-items: center; flex-wrap: wrap; }}
  .badge {{ padding: 6px 18px; border-radius: 4px; font-weight: bold; font-size: 1.1rem; }}
  .badge.pass {{ background: #166534; color: #22c55e; }}
  .badge.fail {{ background: #7f1d1d; color: #ef4444; }}
  .stats-grid {{ display: grid; grid-template-columns: repeat(auto-fit, minmax(110px, 1fr)); gap: 12px; margin: 12px 0; }}
  .stat-label {{ display: block; font-size: 0.75rem; color: #888; }}
  .stat-value {{ display: block; font-size: 1.2rem; font-weight: bold; }}
  .stat-value.ok {{ color: #22c55e; }}
  .stat-value.fail {{ color: #ef4444; }}
  .timeline {{ margin: 8px 0; overflow-x: auto; }}
  .timeline-label {{ font-size: 0.75rem; color: #888; margin-top: 4px; }}
  .dot {{ display: inline-block; width: 10px; height: 10px; border-radius: 50%; }}
  .ok-dot {{ background: #22c55e; }}
  .warn-dot {{ background: #f59e0b; }}
  .fail-dot {{ background: #ef4444; }}
  table {{ width: 100%; border-collapse: collapse; margin: 8px 0; }}
  th, td {{ text-align: left; padding: 6px 12px; border-bottom: 1px solid #333; font-size: 0.85rem; }}
  th {{ color: #888; font-weight: normal; }}
  .added {{ color: #22c55e; }}
  .removed {{ color: #ef4444; }}
  .ok {{ color: #22c55e; }}
  .fail {{ color: #ef4444; }}
  ul {{ list-style: none; padding-left: 8px; }}
  li {{ padding: 2px 0; font-size: 0.85rem; }}
  pre {{ background: #111; padding: 16px; border-radius: 4px; overflow-x: auto; font-size: 0.8rem; white-space: pre-wrap; max-height: 600px; overflow-y: auto; }}
  code {{ font-size: 0.8rem; background: #222; padding: 2px 4px; border-radius: 2px; }}
  details summary {{ cursor: pointer; padding: 8px 0; }}
  details summary:hover {{ color: #fff; }}
</style>
</head>
<body>
<h1>KServe Module Upgrade Test Report</h1>
<h2>{before_ts} &rarr; {after_ts}</h2>

<div class="card">
  <div class="summary">
    {pass_badge}
    <div><span class="stat-label">Total Requests</span><span class="stat-value">{total_reqs:,}</span></div>
    <div><span class="stat-label">Failed</span><span class="stat-value {'fail' if total_fail>0 else ''}">{total_fail:,}</span></div>
    <div><span class="stat-label">Max Downtime</span><span class="stat-value {'fail' if max_downtime>0 else ''}">{max_downtime}s</span></div>
  </div>
</div>

{load_html}
{pod_html}
{res_html}
{cr_html}
{diff_html}

</body>
</html>''')

print(f'Report written to {output_file}')
PYTHON_SCRIPT
